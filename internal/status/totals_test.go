// SPDX-License-Identifier: MIT

package status

import (
	"testing"
	"time"

	"github.com/phil9922/backup-maker/internal/config"
	"github.com/phil9922/backup-maker/internal/localmirror"
	"github.com/phil9922/backup-maker/internal/syncthing"
)

// totalsFor collects a model with the odometer wired to fixed figures.
func totalsFor(t *testing.T, cfg *config.Config, bytes, files uint64, since time.Time) Totals {
	t.Helper()
	col := &Collector{
		Cfg:     func() *config.Config { return cfg },
		Client:  func() *syncthing.Client { return nil },
		Engines: func() []*localmirror.Engine { return nil },
		Totals:  func() (uint64, uint64, time.Time) { return bytes, files, since },
	}
	return col.Collect().Totals
}

// The odometer reaches the model, and so does the target mix that says what it
// covers — without which no renderer can tell an honest figure from a partial
// one.
func TestTotalsCarryTheTargetMix(t *testing.T) {
	since := time.Date(2026, 3, 3, 9, 0, 0, 0, time.UTC)
	got := totalsFor(t, &config.Config{
		General: config.General{MachineName: "workstation"},
		Targets: []config.Target{
			{Type: "drive", Name: "sdcard", Path: "/media/sd"},
			{Type: "share", Name: "nas", URL: "//nas/backups"},
			{Type: "device", Name: "laptop", DeviceID: "AAAA-BBBB"},
		},
	}, 1500, 7, since)

	if got.Bytes != 1500 || got.Files != 7 || !got.Since.Equal(since) {
		t.Fatalf("odometer did not reach the model: %+v", got)
	}
	if got.MirrorTargets != 2 || got.DeviceTargets != 1 {
		t.Fatalf("target mix = %d mirror / %d device, want 2 / 1", got.MirrorTargets, got.DeviceTargets)
	}
	if !got.Counted() {
		t.Error("a machine with drive and share destinations reads as uncounted")
	}
	if !got.Partial() {
		t.Error("a machine that also backs up to another computer does not read as partial")
	}
}

// THE MISLEADING CASE: every destination is a paired computer, so nothing ever
// passes through our copy loop. The figure is genuinely zero, and rendering it
// as "0B backed up" would tell a user with working backups that nothing has
// ever been protected. Counted() is what stops that.
func TestOnlyDeviceTargetsIsNotCountedRatherThanZero(t *testing.T) {
	got := totalsFor(t, &config.Config{
		General: config.General{MachineName: "laptop"},
		Targets: []config.Target{
			{Type: "device", Name: "desktop", DeviceID: "AAAA-BBBB"},
			{Type: "device", Name: "pi", DeviceID: "CCCC-DDDD"},
		},
	}, 0, 0, time.Time{})

	if got.Bytes != 0 || got.Files != 0 {
		t.Fatalf("bytes were invented for a machine that only pairs: %+v", got)
	}
	if got.Counted() {
		t.Fatal("a device-only machine reports a counted zero; the dashboard would print a proud 0B")
	}
	if !got.Partial() {
		t.Error("a device-only machine does not read as partial")
	}
}

// A machine that once had a drive target and no longer does still has a real
// lifetime figure. Having copied something is enough for the number to stand on
// its own.
func TestTotalsStayCountedOnceSomethingHasBeenCopied(t *testing.T) {
	got := totalsFor(t, &config.Config{
		General: config.General{MachineName: "laptop"},
		Targets: []config.Target{{Type: "device", Name: "desktop", DeviceID: "AAAA"}},
	}, 4096, 3, time.Now())
	if !got.Counted() {
		t.Fatal("a machine with real copied bytes reads as uncounted")
	}
}

// Nothing configured at all: no destinations, no history. Neither counted nor
// partial, so the renderers leave the line out entirely rather than reporting
// on a setup that does not exist.
func TestNoTargetsReportsNothingToSay(t *testing.T) {
	got := totalsFor(t, &config.Config{General: config.General{MachineName: "fresh"}}, 0, 0, time.Time{})
	if got.Counted() || got.Partial() {
		t.Fatalf("a machine with no destinations claims something about its backups: %+v", got)
	}
}

// A collector with no odometer wired (an adopted setup, a test harness) must
// leave it at zero rather than panicking on the nil seam.
func TestTotalsToleratesNoCounter(t *testing.T) {
	cfg := &config.Config{
		General: config.General{MachineName: "workstation"},
		Targets: []config.Target{{Type: "drive", Name: "sdcard", Path: "/media/sd"}},
	}
	col := &Collector{
		Cfg:     func() *config.Config { return cfg },
		Client:  func() *syncthing.Client { return nil },
		Engines: func() []*localmirror.Engine { return nil },
	}
	if got := col.Collect().Totals; got.Bytes != 0 || got.MirrorTargets != 1 {
		t.Fatalf("unwired odometer produced %+v", got)
	}
}
