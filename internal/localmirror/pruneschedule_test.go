// SPDX-License-Identifier: MIT

package localmirror

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// seedVersion plants a version-store entry as if keepVersion had made it at
// now-age, and returns its absolute path.
//
// IT ALSO PLANTS THE LIVE FILE the version was made from, which is what makes
// it a SUPERSEDED version rather than the last copy of something. Retention
// refuses to take the last copy by age alone (see Prune), so without a live
// counterpart every one of these would be kept for ever and the schedule tests
// below would be asserting the wrong thing about the wrong file. A version with
// nothing live behind it is its own case, tested in
// TestPruningNeverRemovesTheLastCopyOfAFile.
func seedVersion(t *testing.T, root, rel string, age time.Duration) string {
	t.Helper()
	live := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(live), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(live, []byte("current"), 0o644); err != nil {
		t.Fatal(err)
	}
	return seedOrphanedVersion(t, root, rel, age)
}

// seedOrphanedVersion plants a version with NO live file behind it: the shape
// left when a file is deleted from the source and its destination copy is moved
// into the store. This version is the only copy of that file anywhere.
func seedOrphanedVersion(t *testing.T, root, rel string, age time.Duration) string {
	t.Helper()
	abs := filepath.Join(root, filepath.FromSlash(versionPath(rel, time.Now().Add(-age))))
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(abs, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	return abs
}

// pruneEngine builds an engine over real directories with a valid marker, so
// Run's first pass completes instead of refusing the destination.
func pruneEngine(t *testing.T, maxAgeDays int) (*Engine, string, *countingBackend) {
	t.Helper()
	src, dst := t.TempDir(), t.TempDir()
	write(t, src, "a.txt", "alpha")
	cb := &countingBackend{Backend: NewLocalFS(dst)}
	if err := WriteMarker(cb, "uuid-1", "mach"); err != nil {
		t.Fatal(err)
	}
	e := New(Options{
		FolderID: "f1", TargetName: "dest", SourcePath: src, Backend: cb,
		MachineName: "mach", Label: "proj", UUID: "uuid-1",
		MaxAgeDays: maxAgeDays, Log: quietLog(),
	})
	return e, dst, cb
}

// THE GUARANTEE: the "~30 days of versions" promise does not depend on the
// daemon staying up for 24 hours straight.
//
// Prune used to fire only from a 24-hour ticker, which resets with the
// process — and a machine that upgrades, reboots or suspends daily never
// reaches the first fire, so the version store grew for ever. Found live:
// 24.9GB of versions in four days, including 3,562 copies of one database,
// on a daemon that had been restarted seven times in eighteen hours.
func TestPruningDoesNotRequireADayOfContinuousUptime(t *testing.T) {
	e, dst, _ := pruneEngine(t, 30)
	tooOld := seedVersion(t, dst, "doc.txt", 40*24*time.Hour)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go e.Run(ctx)

	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(tooOld); os.IsNotExist(err) {
			return // pruned within seconds of startup, no 24h wait
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("a 40-day-old version survived engine startup — pruning is still waiting on 24 hours of continuous uptime")
}

// A prune that succeeded is not repeated within the same day: the hourly
// rescan asks pruneIfDue every time, and elapsed-time-since-last-success is
// what keeps that from becoming an hourly walk of the whole version store.
func TestASuccessfulPruneIsNotRepeatedWithinTheDay(t *testing.T) {
	e, dst, cb := pruneEngine(t, 30)
	seedVersion(t, dst, "doc.txt", time.Minute)

	e.pruneIfDue()
	if cb.walks == 0 {
		t.Fatal("the first pruneIfDue never walked the version store — nothing was pruned")
	}
	cb.walks = 0
	e.pruneIfDue()
	if cb.walks != 0 {
		t.Errorf("pruneIfDue walked the store again %d time(s) within the same day", cb.walks)
	}
}

// A hand-edited versioning_max_age_days = 0 must mean "the default", never
// "everything is too old": Prune with a zero maxAge would empty the store.
// Same convention as syncthing.StaggeredVersioning.
func TestAZeroMaxAgeMeansTheDefaultNotDeleteEverything(t *testing.T) {
	e, dst, _ := pruneEngine(t, 0)
	if e.maxAge != 30*24*time.Hour {
		t.Fatalf("maxAge = %v with MaxAgeDays 0, want the 30-day default", e.maxAge)
	}
	kept := seedVersion(t, dst, "doc.txt", 5*24*time.Hour)
	e.pruneIfDue()
	if _, err := os.Stat(kept); err != nil {
		t.Errorf("a 5-day-old version was deleted under a zero max age: %v", err)
	}
}

// THE GUARANTEE: retention never deletes the last copy of a file.
//
// When a file is deleted from the source, its destination copy is not deleted —
// keepVersion moves it into the version store (scan.go). From that moment the
// version IS the file: nothing in the source, nothing in the live mirror. Prune
// then treated it as an ordinary old version and deleted it once it passed
// maxAge, so a file removed from a source folder a month earlier quietly went
// from every destination too. A backup program may not be the thing that
// destroys the last copy of your file.
func TestPruningNeverRemovesTheLastCopyOfAFile(t *testing.T) {
	dst := t.TempDir()
	b := NewLocalFS(dst)

	// Deleted from the source 40 days ago: only the version remains.
	onlyCopy := seedOrphanedVersion(t, dst, "gone.txt", 40*24*time.Hour)
	// Still in the mirror, with an equally old superseded version.
	superseded := seedVersion(t, dst, "kept.txt", 40*24*time.Hour)

	if err := Prune(b, 30*24*time.Hour, time.Now()); err != nil {
		t.Fatal(err)
	}

	if _, err := os.Stat(onlyCopy); os.IsNotExist(err) {
		t.Error("the only remaining copy of a deleted file was pruned away — " +
			"that file no longer exists anywhere")
	}
	if _, err := os.Stat(superseded); !os.IsNotExist(err) {
		t.Error("an old version whose file is still in the mirror was kept; " +
			"retention has stopped working for the ordinary case")
	}
}

// The rule is about the LAST copy, not about age: a deleted file with several
// old versions keeps exactly one, not all of them.
func TestOnlyTheNewestOfAnOrphanedFilesVersionsIsKept(t *testing.T) {
	dst := t.TempDir()
	b := NewLocalFS(dst)
	older := seedOrphanedVersion(t, dst, "gone.txt", 60*24*time.Hour)
	newest := seedOrphanedVersion(t, dst, "gone.txt", 40*24*time.Hour)

	if err := Prune(b, 30*24*time.Hour, time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(newest); os.IsNotExist(err) {
		t.Error("the newest surviving copy was deleted")
	}
	if _, err := os.Stat(older); !os.IsNotExist(err) {
		t.Error("every old version was kept, not just the last copy — the store would grow for ever")
	}
}
