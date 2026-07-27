// SPDX-License-Identifier: MIT

package daemon

// lockResponsive reports whether the daemon's central lock can still be taken.
// It is the liveness signal handed to the systemd watchdog, and TryLock is what
// makes it safe: the probe never blocks, so asking the question can never
// itself be the thing that hangs.
//
// WHY THE LOCK, AND NOT ANYTHING ELSE.
//
// d.mu is taken from a dozen places across this package — config applies, the
// status writer, the archive scheduler, the setup and adopt APIs. A deadlock
// there is the realistic way this daemon wedges while still looking healthy,
// and it is not hypothetical: a change during development produced exactly one,
// where applyConfig held d.mu and then called through to code that wanted it
// again. If the lock is obtainable, the daemon's shared state is not jammed and
// every one of those paths can still run.
//
// DO NOT "IMPROVE" THIS BY GATING ON THE STATUS CYCLE, or on a backup actually
// completing. It is the tempting signal and it is wrong. The status cycle does
// network I/O to SMB destinations, so a NAS that is switched off — or a laptop
// carried out of range — would stall it. systemd would then kill a perfectly
// healthy daemon, and the restart would not fix anything, because nothing was
// broken here: the result is a reboot loop caused by an unplugged drive. A
// watchdog that misjudges liveness is worse than no watchdog.
//
// WHAT THIS RELIES ON, AND WHY IT IS NOT AN ACCIDENT. Nothing may hold d.mu
// across I/O to a destination. applyConfig used to — it opened every drive and
// share and rewrote each adoption manifest with the lock held — which made a
// handful of unreachable shares on a single config reload indistinguishable
// from a deadlock, and would have had systemd kill a daemon whose only problem
// was a NAS that was switched off. It now does all of that first and takes the
// lock only to swap the result in (see applyConfig), so the hold is memory-only
// and bounded by nothing slower than the CPU.
// TestConfigApplyLeavesTheCentralLockFreeWhileADestinationHangs is the guard.
// Any future code that wants both the lock and a destination owes the same
// treatment; the timeouts (5s to dial, 60s per operation, per share) are a
// backstop, not the plan.
func (d *daemon) lockResponsive() bool {
	if !d.mu.TryLock() {
		return false
	}
	d.mu.Unlock()
	return true
}
