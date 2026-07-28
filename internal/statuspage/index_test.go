// SPDX-License-Identifier: MIT

package statuspage

import (
	"strings"
	"testing"
	"time"
)

func TestTheIndexListsEveryMachineOnTheDrive(t *testing.T) {
	now := time.Date(2026, 7, 28, 12, 0, 0, 0, time.UTC)
	page, err := RenderIndex([]IndexEntry{
		{Machine: "my-laptop", Written: now.Add(-2 * time.Minute)},
		{Machine: "attic-pi", Written: now.Add(-10 * time.Minute)},
	}, nil, now)
	if err != nil {
		t.Fatalf("RenderIndex: %v", err)
	}
	html := string(page)

	for _, machine := range []string{"my-laptop", "attic-pi"} {
		if !strings.Contains(html, machine) {
			t.Errorf("%q is missing from the index; that computer's backups are on the drive and unreachable from the page beside them", machine)
		}
		// Named AND linked: a list of names nobody can click leaves the
		// per-machine reports undiscoverable.
		if !strings.Contains(html, machine+"/"+FileName) {
			t.Errorf("no link to %q's own status page", machine)
		}
	}
}

// The index is written by whichever machine happens to be running, so a machine
// that has stopped reporting altogether would otherwise simply be a row with an
// old date on it. Saying "in sync" — or saying nothing — about a computer that
// died last week is the one lie this page exists to avoid.
func TestAMachineThatStoppedWritingIsMarkedStaleOnTheIndex(t *testing.T) {
	now := time.Date(2026, 7, 28, 12, 0, 0, 0, time.UTC)
	page, err := RenderIndex([]IndexEntry{
		{Machine: "my-laptop", Written: now.Add(-time.Minute)},
		{Machine: "dead-box", Written: now.Add(-8 * 24 * time.Hour), Stale: true},
	}, nil, now)
	if err != nil {
		t.Fatalf("RenderIndex: %v", err)
	}
	html := string(page)

	if !strings.Contains(html, "8 days ago") {
		t.Error("the index does not say how long ago the stale machine last reported")
	}
	if !strings.Contains(html, "not reporting") {
		t.Error("a machine that stopped reporting is not called out as such; its row reads like any other")
	}
	// And the healthy one is not tarred with it.
	if strings.Count(html, "not reporting") != 1 {
		t.Errorf("the stale warning was applied to more than the stale machine:\n%s", html)
	}
}

// A destination nobody has written to yet must say so rather than render an
// empty table that reads as "no problems".
func TestAnEmptyIndexSaysThereIsNothingHere(t *testing.T) {
	page, err := RenderIndex(nil, nil, time.Now())
	if err != nil {
		t.Fatalf("RenderIndex: %v", err)
	}
	if !strings.Contains(string(page), "No computer has written a status page here yet") {
		t.Error("an empty destination renders a blank page rather than saying it is empty")
	}
}

func TestAMachinesPageLivesInsideItsOwnDirectory(t *testing.T) {
	// The whole point of the move: two machines' pages are different files.
	if PathFor("my-laptop") == PathFor("attic-pi") {
		t.Fatal("two machines' status pages resolve to the same file, so one erases the other")
	}
	if got := PathFor("my-laptop"); got != "my-laptop/"+FileName {
		t.Errorf("PathFor = %q, want my-laptop/%s", got, FileName)
	}
	// And a name a filesystem cannot take is made safe the same way the mirror
	// makes it safe, or the page would land beside the backups rather than in
	// with them.
	if strings.ContainsAny(PathFor(`a:b*c`), `:*`) {
		t.Errorf("PathFor left characters FAT and NTFS refuse: %q", PathFor(`a:b*c`))
	}
}

// A folder on the same storage that is a destination in its own right gets
// named, but NOT among the computers. Listing it as a machine would link to
// another index and claim a computer exists that does not; leaving it out
// entirely means somebody picking up the drive never finds backups sitting on
// it.
func TestANestedDestinationIsNamedButNotCountedAsAComputer(t *testing.T) {
	now := time.Date(2026, 7, 28, 12, 0, 0, 0, time.UTC)
	page, err := RenderIndex(
		[]IndexEntry{{Machine: "my-laptop", Written: now.Add(-time.Minute)}},
		[]string{"Backups"}, now)
	if err != nil {
		t.Fatalf("RenderIndex: %v", err)
	}
	html := string(page)

	if !strings.Contains(html, `<a href="Backups/`+FileName+`">Backups</a>`) {
		t.Errorf("the nested destination is not reachable from this page:\n%s", html)
	}
	if strings.Contains(html, "<td><a href=\"Backups/") {
		t.Error("the nested destination was listed as though it were a computer")
	}
	if !strings.Contains(html, "reports separately") {
		t.Error("nothing explains why the nested folder is listed apart from the computers")
	}
}
