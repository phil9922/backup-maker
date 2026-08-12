// SPDX-License-Identifier: MIT

package daemon

import (
	"github.com/phil9922/backup-maker/internal/config"
)

// updateState is how everything in this package writes state.json: it changes
// the fields the caller owns on a state read fresh from disk, saves it under
// the cross-process lock, and adopts the result as d.state.
//
// d.mu MUST ALREADY BE HELD. Every caller here mutates or reads d.state around
// the call, and holding the lock across it is also what stops two daemon
// writers swapping d.state back and forth out of order. The lock ordering is
// therefore always d.mu first, then the state file lock — never the reverse,
// and mutate must not reach back for d.mu.
//
// WHY IT IS A CLOSURE OVER A FRESH STATE RATHER THAN d.state.Save(). The daemon
// holds one State for its whole life and used to write the whole of it on every
// flush, which put fifteen fields it does not own back to whatever it last saw.
// That is what undid a destination's rename mid-flight and took the destination
// offline (see config.UpdateState). Now each writer sets only its own fields, so
// a rename, a `set-password`, or anything else written behind our back survives.
//
// The things memory is right about are carried over first, so writing only your
// own fields does not mean losing them:
//
//   - the odometer, which is authoritative in memory and would otherwise wind
//     backwards to whatever a setup command last copied into the file;
//   - the IPC token and the install id, which are minted once and never
//     rotated — adopting an empty one would make this machine a stranger to
//     every claim it holds and stop it backing up to its own drives;
//   - a secret the OS keyring would not give up on THIS read but that we are
//     already holding, which is the case State.KeepReachableSecrets exists for.
//     Without it a keyring that relocks mid-session would strip the share
//     passwords out of a daemon that was using them successfully, on a 30-second
//     timer, and every share destination would stop;
//   - the LAN device list, which nothing outside this package ever writes and
//     which the daemon changes on every page load of the network view — a
//     sighting, a name typed on the holding page, a lapsed request swept up.
//     Those are batched onto the tally's flush rather than saved one by one, so
//     taking the file's copy here would throw away every change since the last
//     flush, including a device that has just been approved.
//
// It is the same treatment applyConfig gives its reload, for the same reasons;
// that path keeps its own copy because it also has a config to apply.
//
// A mutate that returns an error leaves the file alone and passes the error back
// for the caller to report, and d.state stays as it was. The one exception is
// config.ErrStateUnchanged, which means "there turned out to be nothing to
// write": no save happens, but what the file says is adopted, because it was
// read under the lock and is by definition current.
func (d *daemon) updateState(mutate func(*config.State) error) error {
	fresh, err := config.UpdateState(func(s *config.State) error {
		d.carryOverFromMemory(s)
		return mutate(s)
	})
	if err != nil {
		return err
	}
	d.state = fresh
	return nil
}

// carryOverFromMemory copies onto a freshly loaded state the few things this
// process knows better than the file does. See updateState for why each.
func (d *daemon) carryOverFromMemory(s *config.State) {
	if d.state == nil {
		return
	}
	if d.state.IPCToken != "" {
		s.IPCToken = d.state.IPCToken
	}
	if s.InstallID == "" {
		s.InstallID = d.state.InstallID
	}
	s.KeepReachableSecrets(d.state)
	// Only when this process has a list at all: nil means it has never had one,
	// and a daemon built without loading state.json (a test, a hand-made one)
	// must not blank the file's.
	if d.state.LANDevices != nil {
		s.LANDevices = d.state.LANDevices
	}
	if d.tally != nil {
		s.BytesCopiedTotal, s.FilesCopiedTotal, s.CountingSince = d.tally.snapshot()
	}
}
