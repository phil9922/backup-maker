// SPDX-License-Identifier: MIT

package daemon

import (
	"strings"
	"testing"
	"time"

	"github.com/phil9922/backup-maker/internal/config"
	"github.com/phil9922/backup-maker/internal/status"
)

// THE GUARANTEE: a destination nothing has reached for longer than
// stale_after_days raises "backups are not reaching" whatever the reason,
// including storage refused as unrecognised.
//
// "wrong-drive" outranks "stale", so the destination's state never read
// "stale" and the backstop never fired. With unrecognised-storage alerts
// switched off — the case on the machine this was found on — a backup box
// whose disk was missing would have stayed silent for ever.
func TestARefusedDestinationStillAlertsOnceItIsStale(t *testing.T) {
	a := kindsAlerter(config.AlertKinds{UnrecognisedStorage: off()})
	now := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	seen := now.Add(-8 * 24 * time.Hour)
	m := status.Model{
		MachineName: "my-laptop",
		Targets:     []status.TargetInfo{{Name: "backup-pi", Type: "share", State: "wrong-drive", LastSeen: seen}},
		Rows:        []status.Row{{FolderID: "f1", TargetName: "backup-pi", State: "wrong-drive", Stale: true, LastSeen: seen}},
	}
	got := onlyAlert(t, a.pending(m, now))
	if !strings.Contains(got.title, "not reaching backup-pi") {
		t.Fatalf("alert = %q, want backups-not-reaching", got.title)
	}
	if more := a.pending(m, now.Add(time.Minute)); len(more) != 0 {
		t.Fatalf("alerted again while still broken: %s", titles(more))
	}
}

// And not before: a refusal younger than stale_after_days is the
// unrecognised-storage alert's business, which this machine switched off.
func TestARefusedDestinationIsQuietUntilItIsStale(t *testing.T) {
	a := kindsAlerter(config.AlertKinds{UnrecognisedStorage: off()})
	now := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	m := status.Model{
		MachineName: "my-laptop",
		Targets:     []status.TargetInfo{{Name: "backup-pi", Type: "share", State: "wrong-drive", LastSeen: now.Add(-time.Hour)}},
		Rows:        []status.Row{{FolderID: "f1", TargetName: "backup-pi", State: "wrong-drive", LastSeen: now.Add(-time.Hour)}},
	}
	if got := a.pending(m, now); len(got) != 0 {
		t.Fatalf("a fresh refusal raised %s", titles(got))
	}
}
