// SPDX-License-Identifier: MIT

package cmd

import (
	"strings"
	"testing"
	"time"

	"github.com/phil9922/backup-maker/internal/status"
)

func TestHumanBytesBoundaries(t *testing.T) {
	cases := []struct {
		n    int64
		want string
	}{
		{0, "0B"},
		{1, "1B"},
		{1023, "1023B"},
		{1 << 10, "1.0KB"},
		{1536, "1.5KB"},
		{(1 << 20) - 1, "1024.0KB"},
		{1 << 20, "1.0MB"},
		{1 << 30, "1.0GB"},
		{1 << 40, "1.0TB"},
		{1536 * (1 << 30), "1.5TB"},
	}
	for _, c := range cases {
		if got := humanBytes(c.n); got != c.want {
			t.Errorf("humanBytes(%d) = %q, want %q", c.n, got, c.want)
		}
	}
}

func TestHumanFilesGroupsAndAgrees(t *testing.T) {
	cases := []struct {
		n    uint64
		want string
	}{
		{0, "0 files"},
		{1, "1 file"},
		{2, "2 files"},
		{999, "999 files"},
		{1000, "1,000 files"},
		{82391, "82,391 files"},
		{1234567, "1,234,567 files"},
	}
	for _, c := range cases {
		if got := humanFiles(c.n); got != c.want {
			t.Errorf("humanFiles(%d) = %q, want %q", c.n, got, c.want)
		}
	}
}

// The plain, complete case: a machine whose destinations are all ones we write
// ourselves, so the figure covers everything and needs no qualification.
func TestTotalsLineWhenTheFigureIsComplete(t *testing.T) {
	got := totalsLine(status.Totals{
		Bytes:         1536 * (1 << 30),
		Files:         82391,
		Since:         time.Date(2026, 3, 3, 9, 0, 0, 0, time.UTC),
		MirrorTargets: 2,
	})
	want := "Backed up in total: 1.5TB across 82,391 files since 3 March 2026"
	if got != want {
		t.Fatalf("got  %q\nwant %q", got, want)
	}
}

// With a paired computer in the mix the figure is real but incomplete, and says
// so. Reporting it bare would imply it counted the machine-to-machine copies
// too, which it cannot.
func TestTotalsLineSaysWhatItLeavesOut(t *testing.T) {
	got := totalsLine(status.Totals{
		Bytes:         2 << 30,
		Files:         12,
		Since:         time.Date(2026, 3, 3, 0, 0, 0, 0, time.UTC),
		MirrorTargets: 1,
		DeviceTargets: 1,
	})
	if !strings.HasPrefix(got, "Backed up in total: 2.0GB across 12 files since 3 March 2026") {
		t.Fatalf("the figure itself is wrong: %q", got)
	}
	if !strings.Contains(got, "not counted") {
		t.Fatalf("a partial figure did not say what it leaves out: %q", got)
	}
}

// THE ONE THAT MATTERS: a machine that backs up only to other computers has a
// counter of zero because nothing goes through our copy loop. It must never
// read as "0B backed up", which would tell someone with working backups that
// nothing has ever been protected.
func TestTotalsLineNeverShowsAProudZeroOnADeviceOnlyMachine(t *testing.T) {
	got := totalsLine(status.Totals{DeviceTargets: 2})
	if strings.Contains(got, "0B") || strings.Contains(got, "0 files") {
		t.Fatalf("a device-only machine was shown a zero: %q", got)
	}
	if !strings.Contains(got, "not counted on this machine") {
		t.Fatalf("a device-only machine was not told the counter doesn't cover it: %q", got)
	}
	if !strings.Contains(got, "sync engine") {
		t.Fatalf("the line does not say what does the transferring instead: %q", got)
	}
}

// A configured mirror target that has not copied anything yet is a real zero,
// and says so in words rather than as "0B across 0 files".
func TestTotalsLineForAMirrorTargetWithNothingCopiedYet(t *testing.T) {
	got := totalsLine(status.Totals{MirrorTargets: 1, Since: time.Now()})
	if got != "Backed up in total: nothing copied yet" {
		t.Fatalf("got %q", got)
	}
}

// Nothing configured at all: the status command already explains what to do
// next, so the odometer stays quiet rather than reporting on a setup that does
// not exist.
func TestTotalsLineIsSilentWithNoDestinations(t *testing.T) {
	if got := totalsLine(status.Totals{}); got != "" {
		t.Fatalf("got %q, want no line at all", got)
	}
}

// An odometer restored from a state file with no start date (an older install)
// still reports its figure; it just cannot say since when.
func TestTotalsLineOmitsAnUnknownStartDate(t *testing.T) {
	got := totalsLine(status.Totals{Bytes: 1 << 20, Files: 1, MirrorTargets: 1})
	want := "Backed up in total: 1.0MB across 1 file"
	if got != want {
		t.Fatalf("got  %q\nwant %q", got, want)
	}
}

// THE GUARANTEE: the state column answers "are my files safe", not "what is the
// program doing".
//
// A destination holding a complete copy read "scanning" for minutes at a time,
// which leaves the only honest interpretation as "I cannot tell whether I have
// a backup". That is the worst sentence a backup tool can produce, and it was
// producing it while everything was fine. The activity belongs in the progress
// column; this one has to keep its promise.
func TestTheStateColumnSaysWhetherThereIsABackupNotWhatTheProgramIsDoing(t *testing.T) {
	for _, state := range []string{"scanning", "syncing", "in sync"} {
		r := status.Row{State: state, LastSeen: time.Now().Add(-time.Minute)}
		if got := rowState(r); got != "backed up" {
			t.Errorf("a destination holding a copy while %q reads as %q, want \"backed up\"", state, got)
		}
	}
}

// The other half: a destination with no completed pass has no copy to promise,
// and must not claim one. Softening this would be the same failure in reverse.
func TestADestinationWithNoCompletedPassNeverClaimsToBeBackedUp(t *testing.T) {
	for _, state := range []string{"scanning", "syncing"} {
		r := status.Row{State: state, FirstBackup: true}
		if got := rowState(r); got != "first backup" {
			t.Errorf("a destination on its first pass while %q reads as %q, want \"first backup\"", state, got)
		}
	}
}

// A fault still reads as a fault. Reframing must not swallow the states that
// mean something is actually wrong.
func TestAFaultyDestinationStillReadsAsFaulty(t *testing.T) {
	for _, state := range []string{"offline", "stale", "full", "wrong-drive", "name-clash", "error"} {
		r := status.Row{State: state}
		if got := rowState(r); got != state {
			t.Errorf("%q reads as %q; a fault must not be dressed up", state, got)
		}
	}
}
