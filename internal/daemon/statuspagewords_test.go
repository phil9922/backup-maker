// SPDX-License-Identifier: MIT

package daemon

import (
	"strings"
	"testing"
	"time"

	"github.com/phil9922/backup-maker/internal/status"
	"github.com/phil9922/backup-maker/internal/statuspage"
)

// THE GUARANTEE: the page left on a destination uses the same words as
// everything else.
//
// It said "in sync" for two releases after the dashboard and the CLI were taught
// to say "backed up" — and this is the surface you read when the computer it
// describes is off, stolen or broken, which is the moment the vocabulary matters
// most and the moment nobody can go and check another screen.
func TestTheDestinationPageSaysBackedUpLikeEverythingElse(t *testing.T) {
	m := status.Model{
		MachineName: "workstation",
		Rows: []status.Row{
			{FolderLabel: "code", TargetName: "nas", State: "in sync",
				LastSeen: time.Now().Add(-2 * time.Minute)},
			// Mid-scan over a folder that already has a copy: still backed up.
			{FolderLabel: "documents", TargetName: "sdcard", State: "scanning",
				LastSeen: time.Now().Add(-30 * time.Second)},
		},
		Archives: []status.ArchiveRow{
			{Name: "nightly", Target: "nas", State: "due",
				LastRun: time.Now().Add(-20 * time.Hour)},
		},
	}
	built, _ := buildPage(m, time.Now())
	out, err := statuspage.Render(built)
	if err != nil {
		t.Fatal(err)
	}
	page := string(out)

	if strings.Contains(page, "in sync") {
		t.Error(`the page still says "in sync"; every other surface says "backed up"`)
	}
	if n := strings.Count(page, "backed up"); n < 3 {
		t.Errorf(`the page says "backed up" %d times, want 3 (two mirrors and a `+
			`snapshot): %s`, n, page)
	}
	// And the healthy ones are green rather than red. A merely-due snapshot was
	// painted like a failure, because the template compared the state to "ok".
	if strings.Contains(page, `class="bad"`) {
		t.Error(`something healthy is drawn as a fault on the page`)
	}
	if !strings.Contains(page, `class="ok"`) {
		t.Error("nothing on the page is drawn as healthy")
	}
}

// A fault still has to reach the page as a fault. The reframing above must not
// have swallowed it on the way.
func TestTheDestinationPageStillReportsAFault(t *testing.T) {
	m := status.Model{
		MachineName: "workstation",
		Rows: []status.Row{{FolderLabel: "code", TargetName: "nas", State: "offline",
			LastSeen: time.Now().Add(-6 * time.Hour)}},
		Archives: []status.ArchiveRow{{Name: "nightly", Target: "nas", State: "failed",
			LastRun: time.Now().Add(-30 * time.Hour)}},
	}
	built, _ := buildPage(m, time.Now())
	out, err := statuspage.Render(built)
	if err != nil {
		t.Fatal(err)
	}
	page := string(out)
	for _, want := range []string{"offline", "failed", `class="bad"`} {
		if !strings.Contains(page, want) {
			t.Errorf("a broken destination's page is missing %q", want)
		}
	}
	if strings.Contains(page, "backed up") {
		t.Error(`a page with nothing working says "backed up" somewhere`)
	}
}

// Several engine states share one word, so the page is not rewritten when only
// the state behind the word changed. A pass starting over a folder that is
// already backed up says exactly what it said before.
func TestAStateChangeThatChangesNoWordsIsNotAWrite(t *testing.T) {
	at := time.Now()
	idle := status.Model{
		MachineName: "workstation",
		Rows: []status.Row{{FolderLabel: "code", TargetName: "nas", State: "in sync",
			LastSeen: at.Add(-time.Minute)}},
	}
	scanning := idle
	scanning.Rows = []status.Row{{FolderLabel: "code", TargetName: "nas",
		State: "scanning", LastSeen: at.Add(-time.Minute)}}

	_, a := buildPage(idle, at)
	_, b := buildPage(scanning, at)
	if a != b {
		t.Error("a pass starting over an already-backed-up folder changed the page " +
			"fingerprint, so it would earn a write to every destination while saying " +
			"exactly the same thing")
	}
}
