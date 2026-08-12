// SPDX-License-Identifier: MIT

package daemon

import (
	"strings"
	"testing"
	"time"

	"github.com/phil9922/backup-maker/internal/notify"
	"github.com/phil9922/backup-maker/internal/status"
)

// A backup that quietly did less than it claims is the thing worth telling
// somebody about. This one wrote a snapshot on schedule and skipped the read-back
// that proves it can be restored — and every other surface says "backed up",
// truthfully, because it is.
//
// Once, on the way in, with an all-clear when a later run is checked: the same
// discipline every sticky alert here follows.
func TestASnapshotThatWasNotCheckedIsAnnouncedOnceAndWithdrawnOnce(t *testing.T) {
	a, _ := testAlerter(true)
	now := time.Date(2026, 8, 11, 23, 0, 0, 0, time.UTC)
	why := "Reading it back needs about 57.0GB in /tmp and 2.0GB must stay free there, but only 34.0GB is left."

	job := func(unverified bool) status.Model {
		return status.Model{Archives: []status.ArchiveRow{{
			Name: "nightly-home", Target: "nas", State: "ok",
			Unverified: unverified, UnverifiedReason: why,
		}}}
	}

	if got := a.pending(job(false), now); len(got) != 0 {
		t.Fatalf("a checked snapshot alerted: %s", titles(got))
	}

	got := onlyAlert(t, a.pending(job(true), now))
	if got.urgency != notify.Critical {
		t.Errorf("an unchecked snapshot was not critical: %+v", got)
	}
	if !strings.Contains(got.title, "nightly-home") || !strings.Contains(got.title, "not checked") {
		t.Errorf("the alert does not say which schedule, or what happened: %+v", got)
	}
	if !strings.Contains(got.body, why) {
		t.Errorf("the alert drops the reason, so nobody can act on it: %+v", got)
	}
	if !strings.Contains(got.body, "was written") {
		t.Errorf("the alert does not say the snapshot EXISTS, which is the half that is good news: %+v", got)
	}

	// Sticky: saying it again every minute teaches people to dismiss this
	// program unread.
	if again := a.pending(job(true), now); len(again) != 0 {
		t.Fatalf("the same unchecked snapshot alerted twice: %s", titles(again))
	}

	// And the all-clear, once, when a later run is read back in full.
	clear := onlyAlert(t, a.pending(job(false), now))
	if clear.urgency != notify.Normal {
		t.Errorf("good news was delivered as a critical alert: %+v", clear)
	}
	if !strings.Contains(clear.title, "nightly-home") {
		t.Errorf("the all-clear does not name the schedule: %+v", clear)
	}
	if more := a.pending(job(false), now); len(more) != 0 {
		t.Fatalf("the all-clear repeated: %s", titles(more))
	}
}

// A failed snapshot and an unchecked one are different news about the same
// schedule, and neither may swallow the other: one says no backup happened, the
// other that one did and was not proved restorable.
func TestAFailedSnapshotAndAnUncheckedOneAreSeparateNews(t *testing.T) {
	a, _ := testAlerter(true)
	now := time.Date(2026, 8, 11, 23, 0, 0, 0, time.UTC)
	row := func(state string, unverified bool) status.Model {
		return status.Model{Archives: []status.ArchiveRow{{
			Name: "nightly-home", Target: "nas", State: state, Unverified: unverified,
		}}}
	}

	// Written but unchecked, then a run that did not happen at all.
	if got := a.pending(row("ok", true), now); len(got) != 1 {
		t.Fatalf("the unchecked snapshot raised %d alerts, want 1: %s", len(got), titles(got))
	}
	got := a.pending(row("failed", false), now)
	if len(got) != 1 || !strings.Contains(got[0].title, "failed") {
		t.Fatalf("a failure after an unchecked run raised %d alerts: %s", len(got), titles(got))
	}
	// The unchecked state is still owed its all-clear: a failed run is not one.
	if more := a.pending(row("failed", false), now); len(more) != 0 {
		t.Fatalf("the alerts repeated: %s", titles(more))
	}
	clear := a.pending(row("ok", false), now)
	if len(clear) != 2 {
		t.Fatalf("recovering raised %d alerts, want both all-clears: %s", len(clear), titles(clear))
	}
}
