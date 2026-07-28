// SPDX-License-Identifier: MIT

package daemon

import (
	"testing"
)

// The probe systemd's watchdog is gated on. It must say "alive" for a free
// lock, "not right now" for a held one, and — the part that matters — it must
// never block, or the goroutine that exists to notice a wedge would join it.
func TestLockResponsive(t *testing.T) {
	d := &daemon{}
	if !d.lockResponsive() {
		t.Fatal("a free lock did not report as responsive")
	}
	// Twice, because a probe that forgot to release the lock would pass once.
	if !d.lockResponsive() {
		t.Fatal("the probe did not release the lock it took")
	}

	d.mu.Lock()
	if d.lockResponsive() {
		t.Error("a held lock reported as responsive")
	}
	d.mu.Unlock()

	if !d.lockResponsive() {
		t.Error("a lock that was released did not report as responsive again")
	}
}
