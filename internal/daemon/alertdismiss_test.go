// SPDX-License-Identifier: MIT

package daemon

import (
	"testing"
	"time"

	"github.com/phil9922/backup-maker/internal/config"
)

func historyDaemon(t *testing.T, recs ...config.AlertRecord) *daemon {
	t.Helper()
	d := gatedDaemon(t)
	d.state.RecentAlerts = recs
	return d
}

func raised(at time.Time, title string) config.AlertRecord {
	return config.AlertRecord{At: at, Title: title}
}

// What the dashboard shows, which is the thing dismissing is about.
func shown(d *daemon) []config.AlertRecord {
	var out []config.AlertRecord
	for _, a := range d.alertHistory() {
		if !a.Dismissed() {
			out = append(out, a)
		}
	}
	return out
}

// The plain want: an entry under "Recently reported" goes away and stays away.
func TestDismissingAnAlertTakesItOffTheDashboard(t *testing.T) {
	now := time.Now()
	one, two := now.Add(-2*time.Hour), now.Add(-time.Hour)
	d := historyDaemon(t, raised(one, "backup-pi has been stale"), raised(two, "laptopcard is backed up again"))

	if err := d.dismissAlert(one); err != nil {
		t.Fatalf("dismissing: %v", err)
	}

	left := shown(d)
	if len(left) != 1 || left[0].Title != "laptopcard is backed up again" {
		t.Fatalf("the dashboard would still show %+v", left)
	}
	// And it does not come back on the next status cycle.
	if left = shown(d); len(left) != 1 {
		t.Errorf("a dismissed alert reappeared: %+v", left)
	}
}

// THE PROPERTY THE WHOLE DESIGN EXISTS FOR. Dismissing HIDES; it does not
// delete. The history is the only place an alert that was raised and delivered
// nowhere is visible at all — that is the state in which backups can fail
// silently for ever — so tidying a dashboard must never be able to erase it.
func TestDismissingAnAlertNeverDeletesTheRecord(t *testing.T) {
	now := time.Now()
	at := now.Add(-time.Hour)
	d := historyDaemon(t, config.AlertRecord{
		At: at, Title: "backup-pi has been stale", Urgent: true,
		// Delivered nowhere: no method took it, which is the line that says
		// alerting itself is not working.
	})

	if err := d.dismissAlert(at); err != nil {
		t.Fatal(err)
	}

	kept := d.alertHistory() // what `backup-maker alerts` reads
	if len(kept) != 1 {
		t.Fatalf("the record was deleted, not hidden: %+v", kept)
	}
	if !kept[0].Dismissed() {
		t.Error("the record is not marked as dismissed, so the CLI cannot say so")
	}
	if kept[0].Title != "backup-pi has been stale" || !kept[0].Urgent {
		t.Error("dismissing changed what the record says happened")
	}
	if len(kept[0].Delivered) != 0 || len(kept[0].Failed) != 0 {
		t.Error("dismissing changed where the alert got to")
	}
}

// It survives a restart, or "dismissed" would mean "until the daemon restarts"
// and the row would come back with no explanation.
func TestADismissalIsPersisted(t *testing.T) {
	isolateState(t)
	now := time.Now()
	at := now.Add(-time.Hour)

	first := &daemon{state: &config.State{}, cfg: config.New()}
	first.state.RecordAlert(raised(at, "backup-pi has been stale"))
	if err := first.dismissAlert(at); err != nil {
		t.Fatal(err)
	}

	reloaded, err := config.LoadState()
	if err != nil {
		t.Fatalf("reloading: %v", err)
	}
	if len(reloaded.RecentAlerts) != 1 {
		t.Fatalf("history did not survive: %+v", reloaded.RecentAlerts)
	}
	if !reloaded.RecentAlerts[0].Dismissed() {
		t.Error("the dismissal did not survive a restart; the row would come back")
	}
}

// "Dismiss all" clears the section in one press.
func TestDismissAllClearsTheSection(t *testing.T) {
	now := time.Now()
	d := historyDaemon(t,
		raised(now.Add(-3*time.Hour), "one"),
		raised(now.Add(-2*time.Hour), "two"),
		raised(now.Add(-time.Hour), "three"))

	if err := d.dismissAlertsBefore(now.Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	if left := shown(d); len(left) != 0 {
		t.Errorf("Dismiss all left %+v on the dashboard", left)
	}
	if len(d.alertHistory()) != 3 {
		t.Error("Dismiss all deleted records rather than hiding them")
	}
}

// AND IT MUST NOT SWALLOW WHAT NOBODY HAS READ. An alert raised between the
// page rendering and the button being pressed has never been on screen;
// clearing it would be worse than a section that will not clear, because the
// user would never learn it happened.
func TestDismissAllDoesNotSwallowAnAlertRaisedSince(t *testing.T) {
	now := time.Now()
	d := historyDaemon(t,
		raised(now.Add(-2*time.Hour), "seen on the page"),
		raised(now, "arrived while you were clicking"))

	// The page was rendered when the older alert was the newest one.
	if err := d.dismissAlertsBefore(now.Add(-2 * time.Hour)); err != nil {
		t.Fatal(err)
	}

	left := shown(d)
	if len(left) != 1 || left[0].Title != "arrived while you were clicking" {
		t.Fatalf("an unread alert was cleared by Dismiss all: %+v", left)
	}
}

// Dismissing something that is not there is an error rather than a shrug: a
// button that reports success while the row stays put is the bug that gets
// reported as "it doesn't work" with nothing in any log.
func TestDismissingAnAlertThatIsNotThereSaysSo(t *testing.T) {
	now := time.Now()
	d := historyDaemon(t, raised(now.Add(-time.Hour), "the only one"))

	if err := d.dismissAlert(now.Add(-99 * time.Hour)); err == nil {
		t.Error("dismissing a nonexistent alert reported success")
	}
}

// Dismissing twice keeps the first answer, so the CLI reports when it was
// actually dismissed rather than when it was last clicked. Both routes to a
// second dismissal are exercised — the per-alert one and Dismiss all — because
// each keeps its own guard and a test that only drives one leaves the other
// free to overwrite.
func TestDismissingTwiceKeepsTheFirstTime(t *testing.T) {
	now := time.Now()
	at := now.Add(-time.Hour)
	d := historyDaemon(t, raised(at, "one"))
	if err := d.dismissAlert(at); err != nil {
		t.Fatal(err)
	}
	firstTime := d.alertHistory()[0].DismissedAt
	if firstTime.IsZero() {
		t.Fatal("the first dismissal recorded no time")
	}

	// Again, the same way. It is a no-op rather than an error: the alert
	// plainly exists, it is just already gone from the dashboard.
	if err := d.dismissAlert(at); err != nil {
		t.Fatalf("dismissing an already-dismissed alert reported an error: %v", err)
	}
	if got := d.alertHistory()[0].DismissedAt; !got.Equal(firstTime) {
		t.Errorf("a second Dismiss moved the recorded time: %v -> %v", firstTime, got)
	}

	// And again via Dismiss all, which sweeps everything up to a cutoff.
	if err := d.dismissAlertsBefore(now); err != nil {
		t.Fatal(err)
	}
	if got := d.alertHistory()[0].DismissedAt; !got.Equal(firstTime) {
		t.Errorf("Dismiss all moved the recorded time: %v -> %v", firstTime, got)
	}
}

// A new alert after a clear-out still arrives. Dismissing is about what has
// already been said, never a mute.
func TestAnAlertRaisedAfterDismissingEverythingStillShows(t *testing.T) {
	now := time.Now()
	d := historyDaemon(t, raised(now.Add(-time.Hour), "old news"))
	if err := d.dismissAlertsBefore(now); err != nil {
		t.Fatal(err)
	}

	d.state.RecordAlert(raised(now.Add(time.Minute), "backup-pi has been stale"))

	left := shown(d)
	if len(left) != 1 || left[0].Title != "backup-pi has been stale" {
		t.Fatalf("a new alert did not reach the dashboard after a clear-out: %+v", left)
	}
}
