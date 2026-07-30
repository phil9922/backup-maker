// SPDX-License-Identifier: MIT

package status

import (
	"testing"
	"time"
)

// THE GUARANTEE: a destination holding a complete copy says so, whatever the
// engine happens to be doing at the time.
func TestADestinationHoldingACopySaysBackedUp(t *testing.T) {
	for _, state := range []string{"in sync", "scanning", "syncing"} {
		r := Row{State: state}
		if got := RowLabel(r); got != "backed up" {
			t.Errorf("a folder that is %q reads as %q, want \"backed up\"", state, got)
		}
		if got := RowHealth(r); got != "ok" {
			t.Errorf("a folder that is %q is drawn %q, want \"ok\": amber beside the "+
				"word \"backed up\" takes the reassurance straight back", state, got)
		}
	}
}

// The other half. A destination with no completed pass has no copy to promise and
// must not borrow the reassurance; softening this is the same failure in reverse.
func TestAFirstBackupDoesNotClaimToBeBackedUp(t *testing.T) {
	for _, state := range []string{"scanning", "syncing"} {
		r := Row{State: state, FirstBackup: true}
		if got := RowLabel(r); got != "first backup" {
			t.Errorf("a first pass while %q reads as %q, want \"first backup\"", state, got)
		}
		if got := RowHealth(r); got != "busy" {
			t.Errorf("a first pass while %q is drawn %q, want \"busy\"", state, got)
		}
	}
}

// THE GUARANTEE: reframing never swallows a state that means something is wrong.
func TestAFaultKeepsItsOwnNameAndItsRedColour(t *testing.T) {
	for _, state := range []string{"offline", "stale", "full", "wrong-drive", "name-clash", "error"} {
		r := Row{State: state}
		if got := RowLabel(r); got != state {
			t.Errorf("%q reads as %q; a fault must not be dressed up", state, got)
		}
		if got := RowHealth(r); got != "bad" {
			t.Errorf("%q is drawn %q, want \"bad\"", state, got)
		}
	}
}

// Waiting on a person is not a fault, and red sends somebody hunting one that
// does not exist.
func TestWaitingOnAPersonIsNotDrawnAsAFault(t *testing.T) {
	if got := RowHealth(Row{State: "awaiting-pair"}); got != "busy" {
		t.Errorf("awaiting-pair is drawn %q, want \"busy\"", got)
	}
	if got := RowHealth(Row{State: "no destination yet"}); got != "muted" {
		t.Errorf("no destination yet is drawn %q, want \"muted\"", got)
	}
}

// THE GUARANTEE: a snapshot uses the same words as a mirror. The two tables sit
// on one page describing two kinds of backup, and there is no reason one should
// say "ok" where the other says "backed up".
func TestASnapshotUsesTheSameWordsAsAMirror(t *testing.T) {
	ran := time.Date(2026, 7, 28, 3, 0, 0, 0, time.UTC)
	for _, c := range []struct {
		a             ArchiveRow
		label, health string
	}{
		{ArchiveRow{State: "ok", LastRun: ran}, "backed up", "ok"},
		// Due means the NEXT zip is owed, not that the last one is missing. This
		// was painted red on the status page, which made a healthy schedule look
		// like a failure on the one page you read with the computer switched off.
		{ArchiveRow{State: "due", LastRun: ran}, "backed up", "ok"},
		// Packing its second zip: last time's is on the destination the whole time.
		{ArchiveRow{State: "running", LastRun: ran}, "backed up", "ok"},
		// Packing its first: nothing is there yet, and it must not claim otherwise.
		{ArchiveRow{State: "running"}, "first backup", "busy"},
		{ArchiveRow{State: "never run"}, "not backed up yet", "busy"},
		{ArchiveRow{State: "failed", LastRun: ran}, "failed", "bad"},
		// Deliberately stopped is neither working nor broken, so neither colour.
		{ArchiveRow{State: "ok", LastRun: ran, Paused: true}, "backed up", "muted"},
		// Waiting for a password is waiting on a person.
		{ArchiveRow{State: "needs password", NeedsPassword: true}, "needs password", "busy"},
	} {
		if got := ArchiveLabel(c.a); got != c.label {
			t.Errorf("snapshot %q reads as %q, want %q", c.a.State, got, c.label)
		}
		if got := ArchiveHealth(c.a); got != c.health {
			t.Errorf("snapshot %q is drawn %q, want %q", c.a.State, got, c.health)
		}
	}
}

// Every colour this vocabulary can return must be one the surfaces actually
// style. A typo here would render as an unstyled cell — not an error anywhere,
// just a state quietly losing its colour, which is how the dashboard lost all of
// them for two releases.
func TestOnlyKnownColoursAreReturned(t *testing.T) {
	known := map[string]bool{"ok": true, "busy": true, "bad": true, "muted": true}
	states := []string{"in sync", "scanning", "syncing", "offline", "stale", "full",
		"wrong-drive", "name-clash", "error", "awaiting-pair", "no destination yet", ""}
	for _, s := range states {
		for _, first := range []bool{false, true} {
			if c := RowHealth(Row{State: s, FirstBackup: first}); !known[c] {
				t.Errorf("RowHealth(%q, first=%v) = %q, which nothing styles", s, first, c)
			}
		}
	}
	for _, s := range []string{"ok", "due", "running", "preparing", "never run",
		"failed", "needs password", "paused", ""} {
		for _, paused := range []bool{false, true} {
			a := ArchiveRow{State: s, Paused: paused}
			if c := ArchiveHealth(a); !known[c] {
				t.Errorf("ArchiveHealth(%q, paused=%v) = %q, which nothing styles", s, paused, c)
			}
		}
	}
}
