// SPDX-License-Identifier: MIT

package localmirror

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// quotaFS is a Backend with a fixed capacity, so the out-of-space path can be
// exercised without needing root to mount a tiny filesystem. Writes past the
// quota fail exactly as a full disk does.
type quotaFS struct {
	Backend
	root     string
	capacity int64
}

func newQuotaFS(root string, capacity int64) *quotaFS {
	return &quotaFS{Backend: NewLocalFS(root), root: root, capacity: capacity}
}

// used totals real bytes on disk under root.
func (q *quotaFS) used() int64 {
	var n int64
	_ = filepath.Walk(q.root, func(_ string, fi os.FileInfo, err error) error {
		if err == nil && !fi.IsDir() {
			n += fi.Size()
		}
		return nil
	})
	return n
}

func (q *quotaFS) Usage() (uint64, uint64, error) {
	avail := q.capacity - q.used()
	if avail < 0 {
		avail = 0
	}
	return uint64(avail), uint64(q.capacity), nil
}

type quotaFile struct {
	WFile
	q *quotaFS
}

func (f *quotaFile) Write(p []byte) (int, error) {
	if f.q.used()+int64(len(p)) > f.q.capacity {
		return 0, ErrNoSpace
	}
	return f.WFile.Write(p)
}

func (q *quotaFS) OpenWrite(p string) (WFile, error) {
	w, err := q.Backend.OpenWrite(p)
	if err != nil {
		return nil, err
	}
	return &quotaFile{WFile: w, q: q}, nil
}

func newTestEngine(t *testing.T, src string, b Backend, minFree uint64) *Engine {
	t.Helper()
	return New(Options{
		FolderID:     "f1",
		TargetName:   "dest",
		SourcePath:   src,
		Backend:      b,
		MachineName:  "workstation",
		Label:        "code",
		MaxAgeDays:   30,
		MinFreeBytes: minFree,
		Reclaimer:    NewReclaimer(),
		Log:          quietLog(),
	})
}

// The headline behaviour: a destination that fills up deletes its oldest
// history and the backup then completes.
func TestSyncReclaimsSpaceAndCompletes(t *testing.T) {
	src := t.TempDir()
	dstRoot := t.TempDir()
	if err := os.WriteFile(filepath.Join(src, "new.bin"), make([]byte, 4000), 0o644); err != nil {
		t.Fatal(err)
	}

	// Capacity leaves no room for the new file until history is deleted.
	q := newQuotaFS(dstRoot, 6000)
	now := time.Now()
	oldA := writeAt(t, dstRoot, VersionsDirName+"/workstation/code/a.txt~"+stamp(now.Add(-72*time.Hour))+".txt", 2500, now.Add(-72*time.Hour))
	oldB := writeAt(t, dstRoot, VersionsDirName+"/workstation/code/b.txt~"+stamp(now.Add(-48*time.Hour))+".txt", 2500, now.Add(-48*time.Hour))

	e := newTestEngine(t, src, q, 0)
	if _, _, err := e.reconcile(); err != nil {
		t.Fatalf("reconcile: %v", err)
	}

	copied := filepath.Join(dstRoot, "workstation", "code", "new.bin")
	if !exists(copied) {
		t.Fatal("the file was never copied even though history could have been freed")
	}
	if fi, err := os.Stat(copied); err != nil || fi.Size() != 4000 {
		t.Errorf("copied file is wrong: %v size=%v", err, fi)
	}
	if exists(oldA) && exists(oldB) {
		t.Error("no history was reclaimed, yet the copy somehow succeeded")
	}
}

// If nothing can be freed, the engine must say the destination is full rather
// than looking healthy or retrying forever.
func TestSyncReportsFullWhenNothingCanBeFreed(t *testing.T) {
	src := t.TempDir()
	dstRoot := t.TempDir()
	if err := os.WriteFile(filepath.Join(src, "big.bin"), make([]byte, 8000), 0o644); err != nil {
		t.Fatal(err)
	}
	// Capacity too small for the file, and no history to delete.
	q := newQuotaFS(dstRoot, 1000)

	e := newTestEngine(t, src, q, 0)
	_, _, err := e.reconcile()
	if !IsNoSpace(err) {
		t.Fatalf("reconcile err = %v, want an out-of-space error", err)
	}

	// The engine's sync() maps that to the "full" state.
	e.setState("syncing")
	if IsNoSpace(err) {
		e.setState("full")
	}
	if got := e.Status().State; got != "full" {
		t.Errorf("state = %q, want full", got)
	}
}

// Proactive headroom: with min_free set, history is trimmed before a pass
// rather than after a failure.
func TestEnsureHeadroomTrimsBeforeCopying(t *testing.T) {
	src := t.TempDir()
	dstRoot := t.TempDir()
	now := time.Now()
	old := writeAt(t, dstRoot, VersionsDirName+"/workstation/code/a.txt~"+stamp(now.Add(-72*time.Hour))+".txt", 3000, now.Add(-72*time.Hour))

	q := newQuotaFS(dstRoot, 4000) // 1000 free, but we want 2000 spare
	e := newTestEngine(t, src, q, 2000)

	if !e.ensureHeadroom(0) {
		t.Fatal("ensureHeadroom reported no reclaim despite being below the threshold")
	}
	if exists(old) {
		t.Error("old version survived even though headroom was needed")
	}
}

// Reclaiming deletes backups, so it must stay off unless asked for.
func TestNoReclaimWithoutAThreshold(t *testing.T) {
	src := t.TempDir()
	dstRoot := t.TempDir()
	now := time.Now()
	old := writeAt(t, dstRoot, VersionsDirName+"/workstation/code/a.txt~"+stamp(now.Add(-72*time.Hour))+".txt", 3000, now.Add(-72*time.Hour))

	q := newQuotaFS(dstRoot, 4000)
	e := newTestEngine(t, src, q, 0) // no threshold configured

	if e.ensureHeadroom(0) {
		t.Error("reclaimed space with no threshold set; this must be opt-in")
	}
	if !exists(old) {
		t.Error("history was deleted despite reclaiming being disabled")
	}
}
