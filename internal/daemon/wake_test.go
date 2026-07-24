// SPDX-License-Identifier: MIT

package daemon

import (
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/phil9922/backup-maker/internal/config"
	"github.com/phil9922/backup-maker/internal/status"
	"github.com/phil9922/backup-maker/internal/wol"
)

// newTestDaemon builds a daemon with only the fields wakeOfflineTargets uses.
// Packets are pinned to loopback so a test run never broadcasts on the real
// network.
func newTestDaemon(t *testing.T, targets []config.Target) *daemon {
	t.Helper()
	cfg := config.New()
	cfg.Targets = targets
	return &daemon{
		log:   slog.New(slog.NewTextHandler(io.Discard, nil)),
		cfg:   cfg,
		waker: wol.NewWaker(wol.DefaultMinInterval, slog.New(slog.NewTextHandler(io.Discard, nil))),
	}
}

// pinPortsToLoopback keeps magic packets inside this machine for the duration
// of a test.
func pinPortsToLoopback(t *testing.T) {
	t.Helper()
	orig := wol.Ports
	wol.Ports = []int{9}
	t.Cleanup(func() { wol.Ports = orig })
}

const testMAC = "aa:bb:cc:dd:ee:ff"

func wakeableTarget(name, typ string) config.Target {
	return config.Target{
		Type: typ, Name: name, MAC: testMAC,
		WakeBroadcast: "127.0.0.1", // never leaves this machine
		DeviceID:      "ID", URL: "//host/share",
	}
}

func TestWakeOfflineTargets(t *testing.T) {
	pinPortsToLoopback(t)
	now := time.Date(2026, 7, 22, 12, 0, 0, 0, time.UTC)

	cases := []struct {
		name      string
		rowState  string
		wantWoken bool
	}{
		// Connectivity problems: waking is the whole point.
		{"offline target is woken", "offline", true},
		{"stale target is woken", "stale", true},

		// Not connectivity problems. Waking here would be noise at best, and
		// "wrong-drive" specifically means the WRONG storage is present —
		// the machine is plainly already awake.
		{"in-sync target is not woken", "in sync", false},
		{"syncing target is not woken", "syncing", false},
		{"wrong-drive target is not woken", "wrong-drive", false},
		{"errored target is not woken", "error", false},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			d := newTestDaemon(t, []config.Target{wakeableTarget("nas", "share")})
			m := status.Model{Rows: []status.Row{{TargetName: "nas", State: c.rowState}}}

			d.wakeOfflineTargets(m, now)

			woken := !d.waker.LastSent("nas").IsZero()
			if woken != c.wantWoken {
				t.Errorf("state %q: woken=%v, want %v", c.rowState, woken, c.wantWoken)
			}
		})
	}
}

func TestWakeOfflineTargetsSkipsTargetsWithoutMAC(t *testing.T) {
	pinPortsToLoopback(t)
	d := newTestDaemon(t, []config.Target{
		{Type: "share", Name: "nas", URL: "//host/share"}, // no MAC
	})
	m := status.Model{Rows: []status.Row{{TargetName: "nas", State: "offline"}}}

	d.wakeOfflineTargets(m, time.Now())

	if !d.waker.LastSent("nas").IsZero() {
		t.Error("woke a target that never opted in with a MAC")
	}
}

// A drive is attached to this computer; there is no remote machine to wake,
// and config validation rejects the combination. Belt and braces.
func TestWakeOfflineTargetsSkipsLocalDrives(t *testing.T) {
	pinPortsToLoopback(t)
	d := newTestDaemon(t, []config.Target{
		{Type: "drive", Name: "sd", Path: "/media/sd", MAC: testMAC, WakeBroadcast: "127.0.0.1"},
	})
	m := status.Model{Rows: []status.Row{{TargetName: "sd", State: "offline"}}}

	d.wakeOfflineTargets(m, time.Now())

	if !d.waker.LastSent("sd").IsZero() {
		t.Error("woke a local drive target")
	}
}

// Several folders mirror to one target, so an offline target produces several
// rows. It must still get a single packet.
func TestWakeOfflineTargetsRateLimitsAcrossRows(t *testing.T) {
	pinPortsToLoopback(t)
	d := newTestDaemon(t, []config.Target{wakeableTarget("nas", "share")})
	m := status.Model{Rows: []status.Row{
		{TargetName: "nas", FolderID: "f1", State: "offline"},
		{TargetName: "nas", FolderID: "f2", State: "offline"},
		{TargetName: "nas", FolderID: "f3", State: "offline"},
	}}

	d.wakeOfflineTargets(m, time.Now())
	first := d.waker.LastSent("nas")
	if first.IsZero() {
		t.Fatal("target was never woken")
	}

	// A minute later, still offline: inside the 5-minute window, so no
	// second packet.
	d.wakeOfflineTargets(m, first.Add(time.Minute))
	if got := d.waker.LastSent("nas"); !got.Equal(first) {
		t.Errorf("re-sent inside the rate-limit window (last sent moved %v → %v)", first, got)
	}
}

func TestWakeNowRejectsUnwakeableTargets(t *testing.T) {
	pinPortsToLoopback(t)
	d := newTestDaemon(t, []config.Target{
		{Type: "drive", Name: "sd", Path: "/media/sd"},
		{Type: "share", Name: "nas", URL: "//host/share"}, // no MAC
		wakeableTarget("omen", "device"),
	})

	if err := d.WakeNow("sd"); err == nil {
		t.Error("WakeNow accepted a local drive")
	}
	if err := d.WakeNow("nas"); err == nil {
		t.Error("WakeNow accepted a target with no MAC")
	}
	if err := d.WakeNow("nosuch"); err == nil {
		t.Error("WakeNow accepted an unknown target name")
	}
	if err := d.WakeNow("omen"); err != nil {
		t.Errorf("WakeNow rejected a valid device target: %v", err)
	}
}

// The manual command must not be silently swallowed by the background
// loop's rate limit.
func TestWakeNowBypassesRateLimit(t *testing.T) {
	pinPortsToLoopback(t)
	d := newTestDaemon(t, []config.Target{wakeableTarget("omen", "device")})

	if err := d.WakeNow("omen"); err != nil {
		t.Fatal(err)
	}
	first := d.waker.LastSent("omen")

	if err := d.WakeNow("omen"); err != nil {
		t.Fatal(err)
	}
	if d.waker.LastSent("omen").Equal(first) {
		t.Error("a manual wake was suppressed by the rate limit")
	}
}
