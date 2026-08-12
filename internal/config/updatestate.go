// SPDX-License-Identifier: MIT

package config

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"time"

	"github.com/gofrs/flock"
)

// ErrStateUnchanged is what a mutate function returns when it decided there was
// nothing to write after all. UpdateState skips the save and reports no error.
//
// It exists so that "nothing to do" cannot be spelled the same way as a write
// that silently did not happen. A closure that simply returned without touching
// the state would still be saved — harmlessly, but pointlessly, and on the
// daemon's timers that is a file rewritten every few seconds to record nothing.
// Saying so out loud keeps the skip deliberate and visible at the call site.
var ErrStateUnchanged = errors.New("state unchanged")

// stateLockWait is how long UpdateState waits for whoever else is mid-write.
//
// A whole write is a load, a mutate and a rename of a small local file, so the
// realistic wait is microseconds and anything approaching this budget means
// something is wrong rather than busy. It is deliberately shorter than the
// watchdog's grace (see internal/watchdog): the daemon holds d.mu across this
// call, and a wait that outlasted the grace would turn a contended lock into a
// daemon systemd believes has wedged.
const stateLockWait = 5 * time.Second

// stateLockRetry is how often the wait re-tries. flock has no queue, so this is
// a poll; short enough that an uncontended handover costs nothing noticeable.
const stateLockRetry = 20 * time.Millisecond

// stateWriteMu serialises UpdateState within this process.
//
// BOTH LOCKS ARE NEEDED AND NEITHER IS ENOUGH. The file lock below covers the
// other process — a `backup-maker` command run while the daemon is up — but
// flock is per open file description, and this program is its own busiest
// writer: the dashboard's actions run INSIDE the daemon (buildActions wires the
// setup.* functions straight in), so a rename and a tally flush are two
// goroutines of one process racing over one file. This mutex is what makes them
// take turns; the file lock is what makes the CLI take its turn too.
//
// A mutate function must therefore never call UpdateState again — this is a
// plain mutex and a re-entrant call deadlocks. Every closure in the tree is a
// few field assignments for that reason.
var stateWriteMu sync.Mutex

// StateLockPath is the lock file that serialises writes to state.json.
//
// A SIBLING OF daemon.lock RATHER THAN daemon.lock ITSELF, deliberately. That
// one is the single-instance lock: the daemon takes it at startup and holds it
// for its entire life, so a CLI command that wanted it in order to write one
// field would wait for the daemon to exit — which is to say, for ever. This one
// is held for the length of a single read-modify-write and by everybody,
// daemon and CLI alike, which is the only way the two can interleave safely.
func StateLockPath() (string, error) {
	dir, err := Dir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "state.lock"), nil
}

// UpdateState is the one way to change state.json: it takes the lock, re-reads
// the file, applies mutate to what is actually on disk, saves, and returns the
// state it wrote.
//
// WHY EVERY WRITER MUST COME THROUGH HERE. state.json has a dozen independent
// writers — the tally flush every 30 seconds, the alert history, the LAN device
// list, the update check, the archive scheduler, and every setup command — and
// each of them used to load (or worse, hold from startup) a whole State, change
// its own few fields, and write the whole struct back. Every one of those writes
// therefore also rewrote the fifteen fields it does not own, from whatever it
// last saw. On 2026-08-11 that lost a destination's rename: the daemon's flush
// landed a moment after the rename's save and put the file back to the old name,
// so config.toml named a destination that state.json had no UUID and no share
// password for. The daemon logged "target has no recorded UUID; re-add it" and
// that destination backed nothing up for forty minutes without anything on the
// dashboard saying so.
//
// Reading INSIDE the lock is the whole point. A caller that loaded first and
// mutated afterwards has the same race in a smaller window.
//
// THE KEYRING SEAM IS UNCHANGED, and must stay that way: this loads through
// LoadState (which hydrates the secrets out of the OS keyring) and writes
// through Save (which redacts on the way out), which is exactly the pair the
// long comments on those two functions describe as the entire seam. Nothing here
// touches a secret itself.
func UpdateState(mutate func(*State) error) (*State, error) {
	stateWriteMu.Lock()
	defer stateWriteMu.Unlock()

	unlock, err := lockStateFile()
	if err != nil {
		return nil, err
	}
	defer unlock()

	s, err := LoadState()
	if err != nil {
		return nil, err
	}
	if err := mutate(s); err != nil {
		if errors.Is(err, ErrStateUnchanged) {
			return s, nil
		}
		return nil, err
	}
	if err := s.Save(); err != nil {
		return nil, err
	}
	return s, nil
}

// lockStateFile takes the cross-process lock, and reports what it could not do.
//
// IT FAILS TOWARDS THE WRITE HAPPENING. If the lock cannot be taken at all — a
// filesystem that does not implement flock, a config directory that will not
// hold another file — the update goes ahead unlocked rather than being refused.
// The lock exists to stop two writers overwriting each other, which is a
// corruption of one field; refusing to write is a state file that stops
// recording anything at all, which is worse and is the failure this whole change
// exists to remove.
//
// CONTENTION IS THE OTHER CASE AND IS AN ERROR. Somebody holding the lock for
// five seconds is not a filesystem that lacks the feature, it is a writer that
// is stuck or a machine in trouble, and going ahead anyway would be doing the
// exact unlocked read-modify-write this seam was built to end. So it is reported
// to the caller, which either surfaces it (a setup command) or retries on its
// own next tick (the tally). What it is never allowed to be is a silent no-op.
func lockStateFile() (unlock func(), err error) {
	path, err := StateLockPath()
	if err != nil {
		// No config directory means no state.json either; the load below will
		// say so far more clearly than a lock error would.
		return func() {}, nil
	}
	fl := flock.New(path)
	ctx, cancel := context.WithTimeout(context.Background(), stateLockWait)
	defer cancel()
	ok, err := fl.TryLockContext(ctx, stateLockRetry)
	switch {
	case ok:
		return func() { _ = fl.Unlock() }, nil
	case err != nil && ctx.Err() == nil:
		// Locking is unavailable here, not contended. Carry on without it.
		return func() {}, nil
	default:
		return nil, fmt.Errorf("another backup-maker write to state.json has held %s for more than %s; nothing was changed", path, stateLockWait)
	}
}
