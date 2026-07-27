// SPDX-License-Identifier: MIT

package daemon

import (
	"bytes"
	"log/slog"
	"testing"

	"github.com/phil9922/backup-maker/internal/config"
)

func gatedDaemon(t *testing.T) *daemon {
	t.Helper()
	isolateState(t)
	cfg := config.New()
	cfg.General.LANView = true
	cfg.General.LANViewAccess = "approved"
	d := &daemon{
		log:   slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)),
		state: &config.State{},
		cfg:   cfg,
	}
	return d
}

// THE ABUSE THIS BOUNDS. The network view is an unauthenticated listener, and a
// client that keeps no cookies is a brand new device on every request — a
// scanner sweeping the LAN, or anything on the wifi in a loop. Without a cap,
// something outside this machine could grow state.json until the disk filled.
func TestTheWaitingListCannotBeGrownWithoutBound(t *testing.T) {
	d := gatedDaemon(t)

	for range 500 {
		if _, _, issued := d.lanDeviceSeen("", "192.168.1.9", "a device"); issued == "" {
			t.Fatal("no token was issued")
		}
	}
	if n := len(d.state.LANDevices); n > maxPendingLANDevices {
		t.Errorf("500 cookie-less requests left %d records, want at most %d", n, maxPendingLANDevices)
	}
}

// And the cap must never reach an approved device: that record is somebody's
// decision, and losing it silently locks their phone out again.
func TestEvictionNeverForgetsAnApprovedDevice(t *testing.T) {
	d := gatedDaemon(t)

	_, code, token := d.lanDeviceSeen("", "192.168.1.20", "iPhone")
	if err := d.approveLANDevice(code); err != nil {
		t.Fatalf("approving: %v", err)
	}

	for range 200 {
		d.lanDeviceSeen("", "192.168.1.9", "a device")
	}

	approved, gotCode, issued := d.lanDeviceSeen(token, "192.168.1.20", "iPhone")
	if !approved {
		t.Error("an approved device was evicted by a flood of unapproved ones")
	}
	if gotCode != code || issued != "" {
		t.Errorf("the approved device was re-issued: code %q -> %q, issued %q", code, gotCode, issued)
	}
}

// A returning approved device must be recognised by its token, not re-queued.
func TestAnApprovedDeviceIsRecognisedOnReturn(t *testing.T) {
	d := gatedDaemon(t)
	_, code, token := d.lanDeviceSeen("", "192.168.1.30", "Android")
	if err := d.approveLANDevice(code); err != nil {
		t.Fatal(err)
	}
	for range 5 {
		approved, _, issued := d.lanDeviceSeen(token, "192.168.1.30", "Android")
		if !approved || issued != "" {
			t.Fatalf("a returning approved device was challenged again (approved=%v issued=%q)", approved, issued)
		}
	}
	if n := len(d.state.LANDevices); n != 1 {
		t.Errorf("returning visits created %d records, want 1", n)
	}
}

// Revoking takes effect at once — that is the whole point of being able to.
func TestRevokingADeviceLocksItOutImmediately(t *testing.T) {
	d := gatedDaemon(t)
	_, code, token := d.lanDeviceSeen("", "192.168.1.40", "iPad")
	if err := d.approveLANDevice(code); err != nil {
		t.Fatal(err)
	}
	if approved, _, _ := d.lanDeviceSeen(token, "192.168.1.40", "iPad"); !approved {
		t.Fatal("approval did not take")
	}
	if err := d.forgetLANDevice(code); err != nil {
		t.Fatalf("revoking: %v", err)
	}
	approved, _, issued := d.lanDeviceSeen(token, "192.168.1.40", "iPad")
	if approved {
		t.Error("a revoked device still reads the network view")
	}
	if issued == "" {
		t.Error("the revoked device was not treated as new; it would see a stale code")
	}
}
