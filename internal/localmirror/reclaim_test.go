// SPDX-License-Identifier: MIT

package localmirror

import (
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func quietLog() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// writeAt creates a file of the given size with a specific modification time.
func writeAt(t *testing.T, root, rel string, size int, mod time.Time) string {
	t.Helper()
	full := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, make([]byte, size), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(full, mod, mod); err != nil {
		t.Fatal(err)
	}
	return full
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// stamp formats a time the way versionPath does, so tests build realistic
// version filenames.
func stamp(ts time.Time) string { return ts.Format(stampLayout) }

// THE rule. The live mirror is the backup; reclaiming space must never reach
// it, no matter how much room is demanded.
func TestReclaimNeverTouchesTheLiveMirror(t *testing.T) {
	root := t.TempDir()
	b := NewLocalFS(root)
	now := time.Now()

	live := writeAt(t, root, "workstation/code/important.txt", 5000, now)
	marker := writeAt(t, root, MarkerName, 100, now)
	old := writeAt(t, root, VersionsDirName+"/workstation/code/important.txt~"+stamp(now.Add(-72*time.Hour))+".txt", 1000, now.Add(-72*time.Hour))

	// Demand far more than exists, so the reclaimer exhausts everything it is
	// permitted to delete.
	NewReclaimer().Reclaim(b, 1<<40, now, quietLog())

	if !exists(live) {
		t.Error("the live mirror was deleted — this is the backup itself")
	}
	if !exists(marker) {
		t.Error("the target marker was deleted; the destination would be unrecognisable")
	}
	if exists(old) {
		t.Error("the old version should have been reclaimed")
	}
}

// A timed backup whose only snapshot is deleted has no protection at all.
func TestReclaimKeepsTheNewestSnapshotOfEachJob(t *testing.T) {
	root := t.TempDir()
	b := NewLocalFS(root)
	now := time.Now()

	dir := ArchivesDirName + "/workstation/weekly-code"
	oldest := writeAt(t, root, dir+"/weekly-code-20260101-000000.zip", 1000, now.Add(-72*time.Hour))
	middle := writeAt(t, root, dir+"/weekly-code-20260108-000000.zip", 1000, now.Add(-48*time.Hour))
	newest := writeAt(t, root, dir+"/weekly-code-20260115-000000.zip", 1000, now.Add(-24*time.Hour))

	NewReclaimer().Reclaim(b, 1<<40, now, quietLog())

	if !exists(newest) {
		t.Error("the newest snapshot was deleted, leaving the job with no protection")
	}
	if exists(oldest) || exists(middle) {
		t.Error("older snapshots should have been reclaimed")
	}
}

func TestReclaimKeepsASoleSnapshot(t *testing.T) {
	root := t.TempDir()
	b := NewLocalFS(root)
	now := time.Now()
	only := writeAt(t, root, ArchivesDirName+"/workstation/nightly/nightly-20260101-000000.zip",
		9000, now.Add(-96*time.Hour))

	NewReclaimer().Reclaim(b, 1<<40, now, quietLog())

	if !exists(only) {
		t.Error("a job's only snapshot was deleted")
	}
}

// One busy folder must not be able to consume every other folder's history.
func TestReclaimSpreadsAcrossFolders(t *testing.T) {
	root := t.TempDir()
	b := NewLocalFS(root)
	now := time.Now()

	// "code" has three versions, "photos" has one. Freeing 2000 bytes should
	// take one from each rather than two from code.
	var codeFiles []string
	for i := 1; i <= 3; i++ {
		ts := now.Add(-time.Duration(i*24) * time.Hour)
		codeFiles = append(codeFiles, writeAt(t, root,
			VersionsDirName+"/workstation/code/f.txt~"+stamp(ts)+".txt", 1000, ts))
	}
	photoTS := now.Add(-100 * time.Hour)
	photo := writeAt(t, root,
		VersionsDirName+"/workstation/photos/p.jpg~"+stamp(photoTS)+".jpg", 1000, photoTS)

	freed, deleted := NewReclaimer().Reclaim(b, 2000, now, quietLog())
	if freed < 2000 || deleted != 2 {
		t.Fatalf("freed %d bytes in %d deletions, want >=2000 in 2", freed, deleted)
	}

	// The oldest of each group goes first: code's 3-day-old file and photos'.
	if exists(codeFiles[2]) {
		t.Error("code's oldest version survived while a newer one was taken")
	}
	if exists(photo) {
		t.Error("photos kept its history while code gave up two — deletion was not spread")
	}
	if !exists(codeFiles[0]) {
		t.Error("code's newest version was deleted despite the budget being met")
	}
}

func TestReclaimStopsOnceEnoughIsFreed(t *testing.T) {
	root := t.TempDir()
	b := NewLocalFS(root)
	now := time.Now()

	var files []string
	for i := 1; i <= 5; i++ {
		ts := now.Add(-time.Duration(i*24) * time.Hour)
		files = append(files, writeAt(t, root,
			VersionsDirName+"/workstation/code/f.txt~"+stamp(ts)+".txt", 1000, ts))
	}

	freed, deleted := NewReclaimer().Reclaim(b, 1500, now, quietLog())
	if deleted != 2 || freed != 2000 {
		t.Fatalf("freed %d in %d deletions; wanted to stop at 2 once 1500 was covered", freed, deleted)
	}
	remaining := 0
	for _, f := range files {
		if exists(f) {
			remaining++
		}
	}
	if remaining != 3 {
		t.Errorf("%d versions left, want 3 — it deleted more than it needed", remaining)
	}
}

// Nothing to delete is a real state and must be reported honestly rather than
// looping or pretending success.
func TestReclaimReportsWhenNothingIsSafeToDelete(t *testing.T) {
	root := t.TempDir()
	b := NewLocalFS(root)
	writeAt(t, root, "workstation/code/live.txt", 5000, time.Now())

	r := NewReclaimer()
	freed, deleted := r.Reclaim(b, 1<<30, time.Now(), quietLog())
	if freed != 0 || deleted != 0 {
		t.Fatalf("freed %d/%d from a destination with no history", freed, deleted)
	}
	_, text := r.LastOutcome()
	if !strings.Contains(text, "nothing left") {
		t.Errorf("outcome %q should say plainly that nothing could be freed", text)
	}
}

func TestReclaimOutcomeIsRecordedForTheDashboard(t *testing.T) {
	root := t.TempDir()
	b := NewLocalFS(root)
	now := time.Now()
	ts := now.Add(-48 * time.Hour)
	writeAt(t, root, VersionsDirName+"/workstation/code/f.txt~"+stamp(ts)+".txt", 2<<20, ts)

	r := NewReclaimer()
	r.Reclaim(b, 1<<20, now, quietLog())

	when, text := r.LastOutcome()
	if when.IsZero() {
		t.Error("reclaim time was not recorded")
	}
	// Deleting a user's backup history must never be silent.
	if !strings.Contains(text, "freed") || !strings.Contains(text, "MB") {
		t.Errorf("outcome %q should state what was freed", text)
	}
}

func TestFolderOfExtractsTheFolderFromAVersionPath(t *testing.T) {
	got := folderOf(VersionsDirName + "/workstation/code/sub/f.txt~20260101-000000.txt")
	if got != "workstation/code" {
		t.Errorf("folderOf = %q, want workstation/code", got)
	}
}

func TestIsNoSpaceIgnoresUnrelatedErrors(t *testing.T) {
	// Reclaiming deletes backups, so it must never fire on an unrelated fault.
	if IsNoSpace(nil) {
		t.Error("nil counted as out-of-space")
	}
	if IsNoSpace(os.ErrPermission) {
		t.Error("a permission error counted as out-of-space")
	}
	if !IsNoSpace(ErrNoSpace) {
		t.Error("ErrNoSpace not recognised")
	}
}

func TestLocalFSReportsUsage(t *testing.T) {
	b := NewLocalFS(t.TempDir())
	sr, ok := b.(SpaceReporter)
	if !ok {
		t.Fatal("localFS should implement SpaceReporter")
	}
	avail, total, err := sr.Usage()
	if err != nil {
		t.Fatal(err)
	}
	if total == 0 || avail > total {
		t.Errorf("implausible usage: %d avail of %d total", avail, total)
	}
}
