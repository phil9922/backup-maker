// SPDX-License-Identifier: MIT

package status

import (
	"testing"

	"github.com/phil9922/backup-maker/internal/config"
	"github.com/phil9922/backup-maker/internal/localmirror"
	"github.com/phil9922/backup-maker/internal/syncthing"
)

func refusedCollector(refused map[string]string) *Collector {
	cfg := &config.Config{
		General: config.General{MachineName: "workstation"},
		Targets: []config.Target{
			{Type: "drive", Name: "sdcard", Path: "/mnt/sd"},
		},
	}
	return &Collector{
		Cfg:     func() *config.Config { return cfg },
		Client:  func() *syncthing.Client { return nil },
		Engines: func() []*localmirror.Engine { return nil },
		Refused: func() map[string]string { return refused },
	}
}

func stateOf(t *testing.T, m Model, name string) string {
	t.Helper()
	for _, ti := range m.Targets {
		if ti.Name == name {
			return ti.State
		}
	}
	t.Fatalf("no target called %q in the model", name)
	return ""
}

// THE CONTRADICTION THIS EXISTS TO REMOVE. mayWrite discovers within the minute
// that the storage at a mount point is not the storage this target was set up
// against; the mirror engine only finds out when it next tries to write, which
// may be an hour away. In between, the dashboard said "Everything is backed up"
// while an alert underneath said nothing was being written there.
func TestADestinationBeingRefusedReadsAsAFaultBeforeTheEngineNotices(t *testing.T) {
	col := refusedCollector(map[string]string{"sdcard": "wrong-drive"})
	// No engines and no rows: rollUp would call this "no folders assigned",
	// which is muted. The refusal must still win.
	if got := stateOf(t, col.Collect(), "sdcard"); got != "wrong-drive" {
		t.Errorf("state = %q, want wrong-drive — the refusal never reached the model", got)
	}
}

func TestAClaimedMachineDirectoryReadsAsANameClash(t *testing.T) {
	col := refusedCollector(map[string]string{"sdcard": "name-clash"})
	if got := stateOf(t, col.Collect(), "sdcard"); got != "name-clash" {
		t.Errorf("state = %q, want name-clash", got)
	}
}

// Nothing refused must change nothing. The override is only ever allowed to
// make an answer worse, so a healthy machine has to be untouched by it.
func TestNothingIsRefusedLeavesTheStateAlone(t *testing.T) {
	plain := refusedCollector(nil).Collect()
	none := refusedCollector(map[string]string{}).Collect()
	if a, b := stateOf(t, plain, "sdcard"), stateOf(t, none, "sdcard"); a != b {
		t.Errorf("an empty refusal map changed the state: %q vs %q", a, b)
	}
	if got := stateOf(t, plain, "sdcard"); got == "wrong-drive" || got == "name-clash" {
		t.Errorf("a destination with nothing refused reads as %q", got)
	}
}

// A collector that never sets Refused — every test and every caller written
// before this seam existed — must behave exactly as it did.
func TestACollectorWithNoRefusedSeamStillWorks(t *testing.T) {
	col := refusedCollector(nil)
	col.Refused = nil
	if got := stateOf(t, col.Collect(), "sdcard"); got == "" {
		t.Error("a collector with no Refused func produced no state at all")
	}
}

// OFFLINE IS NOT OVERRIDDEN, and this is the subtle one. The refusal flag
// deliberately survives a foreign drive being unplugged, so that the alert is
// not raised again when it returns. A destination we cannot see is one we
// cannot claim anything about: saying "wrong storage is there" about an empty
// USB socket is a fault report about hardware that is absent.
func TestAnUnpluggedDestinationIsNotRelabelledAsWrongStorage(t *testing.T) {
	if got := applyRefusal("offline", "wrong-drive"); got != "offline" {
		t.Errorf("state = %q, want offline — an absent drive was reported as wrong storage", got)
	}
}

// The override only ever makes things worse: a destination already reporting a
// more serious fault keeps it, and a refusal never quietly improves anything.
func TestTheRefusalOverrideOnlyEverMakesTheAnswerWorse(t *testing.T) {
	for _, c := range []struct{ state, refused, want string }{
		{"in sync", "wrong-drive", "wrong-drive"},
		{"syncing", "name-clash", "name-clash"},
		{"no folders assigned", "wrong-drive", "wrong-drive"},
		{"error", "wrong-drive", "error"},    // already worse, kept
		{"in sync", "", "in sync"},           // nothing refused, untouched
		{"stale", "", "stale"},               // nothing refused, untouched
		{"offline", "name-clash", "offline"}, // absent wins, see above
	} {
		if got := applyRefusal(c.state, c.refused); got != c.want {
			t.Errorf("applyRefusal(%q, %q) = %q, want %q", c.state, c.refused, got, c.want)
		}
	}
}
