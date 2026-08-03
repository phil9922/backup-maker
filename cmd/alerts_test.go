// SPDX-License-Identifier: MIT

package cmd

import (
	"strings"
	"testing"
	"time"

	"github.com/phil9922/backup-maker/internal/config"
)

// THE HALF OF THE DESIGN THAT MAKES HIDING SAFE. Dismissing an alert clears it
// off the dashboard and nothing more — this listing still has it, still marked,
// and still saying where it got to. If tidying a page could take an alert out
// of here as well, then "delivered nowhere" — the only visible sign that
// alerting itself has stopped working — would be clearable by somebody who had
// no idea that is what they were doing.
func TestADismissedAlertIsStillListedAndSaysSo(t *testing.T) {
	at := time.Date(2026, 8, 3, 10, 0, 0, 0, time.Local)
	out := strings.Join(alertLines(config.AlertRecord{
		At: at, Title: "backups to backup-pi are stale", Urgent: true,
		DismissedAt: at.Add(time.Hour),
		// Delivered and Failed both empty: raised into silence.
	}), "\n")

	if !strings.Contains(out, "backups to backup-pi are stale") {
		t.Fatalf("a dismissed alert vanished from the listing:\n%s", out)
	}
	if !strings.Contains(out, "(dismissed)") {
		t.Errorf("it is listed but not marked, so the listing disagrees with the dashboard:\n%s", out)
	}
	if !strings.Contains(out, "delivered nowhere") {
		t.Errorf("dismissing hid where the alert got to — the one line worth keeping:\n%s", out)
	}
	if !strings.HasPrefix(out, "!!") {
		t.Errorf("dismissing downgraded an urgent alert:\n%s", out)
	}
}

// An alert nobody has dismissed carries no marker — the noise would make the
// mark meaningless on the machines where it matters.
func TestAnUndismissedAlertIsNotMarked(t *testing.T) {
	out := strings.Join(alertLines(config.AlertRecord{
		At: time.Now(), Title: "laptopcard is backed up again",
		Delivered: []string{"desktop"},
	}), "\n")

	if strings.Contains(out, "dismissed") {
		t.Errorf("an alert nobody dismissed is marked as dismissed:\n%s", out)
	}
	if !strings.Contains(out, "delivered by desktop") {
		t.Errorf("the delivery line is missing:\n%s", out)
	}
}
