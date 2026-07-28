// SPDX-License-Identifier: MIT

package daemon

import (
	"context"
	"sync"
	"time"

	"github.com/phil9922/backup-maker/internal/config"
)

// tallyFlushEvery is how often accumulated copying is written to state.json.
//
// The trade is deliberate: a crash loses at most this much of the counter,
// which nobody will ever notice, while saving state.json once per copied file
// would rewrite the same small file thousands of times during a first sync or a
// restore. State that is intact but a few seconds behind beats state that is
// current and occasionally torn.
const tallyFlushEvery = 30 * time.Second

// tally is the lifetime odometer's in-memory half: it accumulates from the
// mirror engines and the snapshot writer, and persists in batches.
//
// Memory is authoritative while the daemon runs. state.json is reloaded during
// applyConfig (setup commands write UUIDs and credentials there behind our
// back), and a reload that carried an older count would otherwise roll the
// odometer backwards; flushing from memory means the next write puts the true
// figure back.
type tally struct {
	mu    sync.Mutex
	bytes uint64
	files uint64
	since time.Time
	dirty bool

	// persist writes the totals wherever they live. A seam, so the counter can
	// be tested without a state.json and so this type never needs to know how
	// the daemon locks its state.
	persist func(bytes, files uint64, since time.Time) error
}

// newTally seeds the counter from persisted state, so a restart continues the
// odometer rather than starting a fresh one.
//
// A machine that has never counted gets its start date stamped now: "since 3
// March 2026" is only meaningful if the date is the day counting actually
// began, and back-dating it to the install would claim coverage of copies that
// were never counted.
func newTally(s *config.State, persist func(bytes, files uint64, since time.Time) error) *tally {
	t := &tally{
		bytes:   s.BytesCopiedTotal,
		files:   s.FilesCopiedTotal,
		since:   s.CountingSince,
		persist: persist,
	}
	if t.since.IsZero() {
		t.since = time.Now()
		t.dirty = true
	}
	return t
}

// add records files that reached a destination intact. Called from every mirror
// engine and from the snapshot writer, so it does nothing but arithmetic under
// a mutex: the disk write happens on the flush cadence.
func (t *tally) add(files int, bytes int64) {
	if files <= 0 && bytes <= 0 {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if files > 0 {
		t.files += uint64(files)
	}
	if bytes > 0 {
		t.bytes += uint64(bytes)
	}
	t.dirty = true
}

// touch records that state.json has something unwritten that did not come from
// the counters — the mirror sync marks, which ride this same flush rather than
// running a second timer against the same file. Without it, the ordinary case
// of a sync pass that copied nothing would leave the clock it just advanced on
// the floor until some file happened to change.
func (t *tally) touch() {
	if t == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.dirty = true
}

// snapshot reads the live totals for the status model.
func (t *tally) snapshot() (bytes, files uint64, since time.Time) {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.bytes, t.files, t.since
}

// flush writes the totals out if anything has changed since the last write.
func (t *tally) flush() {
	t.mu.Lock()
	if !t.dirty || t.persist == nil {
		t.mu.Unlock()
		return
	}
	bytes, files, since := t.bytes, t.files, t.since
	t.dirty = false
	t.mu.Unlock()

	if err := t.persist(bytes, files, since); err != nil {
		// Put the flag back so the next tick retries rather than silently
		// dropping the interval's copying on the floor.
		t.mu.Lock()
		t.dirty = true
		t.mu.Unlock()
	}
}

// run flushes on a timer until ctx ends, then once more on the way out so an
// orderly shutdown keeps everything it counted.
func (t *tally) run(ctx context.Context) {
	tick := time.NewTicker(tallyFlushEvery)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			t.flush()
			return
		case <-tick.C:
			t.flush()
		}
	}
}

// saveState is the tally's persist seam, wired to the daemon's own state. It
// takes the same lock everything else that touches d.state does, so a config
// reload swapping d.state out cannot race with a flush.
//
// It writes everything state.json batches, not the counters alone: the mirror
// sync marks share this write because they share the file and the flush
// cadence, and two writers renaming the same file on the same timer would be a
// second chance to tear it for no gain. Memory wins over the file for both, so a
// state.json a setup command rewrote behind our back cannot wind either
// backwards.
func (d *daemon) saveState(bytes, files uint64, since time.Time) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.state.BytesCopiedTotal = bytes
	d.state.FilesCopiedTotal = files
	d.state.CountingSince = since
	if d.marks != nil {
		d.state.MirrorLastSync = d.marks.snapshot()
	}
	return d.state.Save()
}

// countCopied is handed to every mirror engine as Options.Counted. Nil-safe
// because engines are also built in tests and by paths that have no tally.
func (d *daemon) countCopied(bytes int64) {
	if d.tally != nil {
		d.tally.add(1, bytes)
	}
}

// totals feeds the status collector.
func (d *daemon) totals() (bytes, files uint64, since time.Time) {
	if d.tally == nil {
		return 0, 0, time.Time{}
	}
	return d.tally.snapshot()
}
