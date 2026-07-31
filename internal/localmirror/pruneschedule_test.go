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
func seedVersion(t *testing.T, root, rel string, age time.Duration) string {
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
