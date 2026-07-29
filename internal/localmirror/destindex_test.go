// SPDX-License-Identifier: MIT

package localmirror

import (
	"errors"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"sync/atomic"
	"testing"
	"time"
)

// countingFS counts the destination round trips a pass makes, which is the
// whole point of the index: the old path made one Stat per source file.
type countingFS struct {
	Backend
	stats atomic.Int64
	walks atomic.Int64
	// failWalkAt aborts the walk after this many entries, standing in for a
	// share that drops mid-listing.
	failWalkAt int
	seen       int
}

func (c *countingFS) Stat(p string) (os.FileInfo, error) {
	c.stats.Add(1)
	return c.Backend.Stat(p)
}

func (c *countingFS) WalkDir(root string, fn fs.WalkDirFunc) error {
	c.walks.Add(1)
	if c.failWalkAt == 0 {
		return c.Backend.WalkDir(root, fn)
	}
	return c.Backend.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		c.seen++
		if c.seen > c.failWalkAt {
			return errors.New("connection reset by peer")
		}
		return fn(p, d, err)
	})
}

// statPerFile is what the engine used to do: ask the destination about every
// source file in turn. Kept here as the reference the index must agree with.
func statPerFile(b Backend, srcInfo os.FileInfo, destPath string) bool {
	di, err := b.Stat(destPath)
	if err != nil {
		return true
	}
	if di.Size() != srcInfo.Size() {
		return true
	}
	delta := srcInfo.ModTime().Sub(di.ModTime())
	if delta < 0 {
		delta = -delta
	}
	return delta > mtimeTolerance
}

// mixedTree writes a source and a destination that differ in every way the
// copy decision cares about.
func mixedTree(t *testing.T) (src, dstRoot string, engine *Engine, counter *countingFS) {
	t.Helper()
	src = t.TempDir()
	dstRoot = t.TempDir()

	write := func(dir, name, body string) {
		t.Helper()
		full := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	// identical on both sides → must not be copied
	write(src, "same.txt", "hello")
	// different size → must be copied
	write(src, "grown.txt", "hello world")
	// only in the source → must be copied
	write(src, "nested/new.txt", "fresh")
	// deep, to prove directories are walked
	write(src, "a/b/c/deep.txt", "deep")

	counter = &countingFS{Backend: NewLocalFS(dstRoot)}
	engine = New(Options{
		FolderID: "f1", TargetName: "dest", SourcePath: src,
		Backend: counter, MachineName: "workstation", Label: "code",
		MaxAgeDays: 30, Reclaimer: NewReclaimer(), Log: quietLog(),
	})

	// Seed the destination as the engine would lay it out.
	destDir := filepath.Join(dstRoot, filepath.FromSlash(engine.destRoot))
	write(destDir, "same.txt", "hello")
	write(destDir, "grown.txt", "hi")
	write(destDir, "a/b/c/deep.txt", "deep")
	// Only on the destination: the cleanup walk's business, not the index's.
	write(destDir, "gone.txt", "old")

	// Match mtimes for the file that must be left alone.
	si, err := os.Stat(filepath.Join(src, "same.txt"))
	if err != nil {
		t.Fatal(err)
	}
	for _, rel := range []string{"same.txt", "a/b/c/deep.txt"} {
		s, err := os.Stat(filepath.Join(src, filepath.FromSlash(rel)))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(filepath.Join(destDir, filepath.FromSlash(rel)), s.ModTime(), s.ModTime()); err != nil {
			t.Fatal(err)
		}
	}
	_ = si
	return src, dstRoot, engine, counter
}

// THE GUARANTEE: deciding what to copy asks the destination once per directory,
// not once per file.
//
// This is the whole fix. Over SMB a Stat is a round trip, and 70,821 of them
// measured 8 to 15 minutes before a byte could move — every time, including the
// passes that only confirmed a destination already holding a complete copy.
func TestDecidingWhatToCopyListsTheDestinationInsteadOfStattingEveryFile(t *testing.T) {
	_, _, e, counter := mixedTree(t)

	counter.stats.Store(0)
	counter.walks.Store(0)
	if _, _, err := e.reconcile(); err != nil {
		t.Fatal(err)
	}

	// Four source files. The old path cost at least one Stat each just to
	// decide; anything near that here means the index is not being consulted.
	// Copies legitimately Stat (copyFile checks for an existing version), so
	// this is bounded rather than zero.
	if got := counter.stats.Load(); got > 4 {
		t.Errorf("destination was stat'd %d times for 4 source files; the decision should come from the listing", got)
	}
	if counter.walks.Load() == 0 {
		t.Error("the destination was never listed, so the index cannot have been built")
	}
}

// THE GUARANTEE: the index picks exactly the files a stat-per-file pass would.
//
// A faster decision that decides differently is not an optimisation. Missing a
// file here means it is silently never backed up.
func TestTheIndexPicksExactlyTheFilesAStatPerFileWould(t *testing.T) {
	src, _, e, counter := mixedTree(t)

	idx, err := e.buildDestIndex()
	if err != nil {
		t.Fatal(err)
	}

	var fromIndex, fromStat []string
	err = filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, rerr := filepath.Rel(src, p)
		if rerr != nil {
			return rerr
		}
		rel = filepath.ToSlash(rel)
		info, ierr := d.Info()
		if ierr != nil {
			return ierr
		}
		if e.shouldCopyIndexed(info, rel, idx) {
			fromIndex = append(fromIndex, rel)
		}
		if statPerFile(counter, info, path.Join(e.destRoot, rel)) {
			fromStat = append(fromStat, rel)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(fromIndex)
	sort.Strings(fromStat)

	if len(fromIndex) != len(fromStat) {
		t.Fatalf("index picked %v, stat-per-file picked %v", fromIndex, fromStat)
	}
	for i := range fromIndex {
		if fromIndex[i] != fromStat[i] {
			t.Fatalf("index picked %v, stat-per-file picked %v", fromIndex, fromStat)
		}
	}
	// And the answer is not vacuously empty.
	if len(fromIndex) == 0 {
		t.Fatal("nothing was picked at all, so the comparison proves nothing")
	}
}

// THE GUARANTEE: a destination that cannot be listed fails the pass instead of
// looking empty.
//
// A missing entry in the index means "copy it". A half-built index would
// therefore recopy most of the folder onto a destination that is already
// struggling — and would then reach the cleanup walk, which versions away
// anything absent from the source, on the strength of a listing that stopped
// early.
func TestADestinationThatCannotBeListedFailsThePassInsteadOfRecopyingEverything(t *testing.T) {
	_, _, e, counter := mixedTree(t)
	counter.failWalkAt = 2

	if _, err := e.buildDestIndex(); err == nil {
		t.Fatal("a walk that died mid-listing produced an index instead of an error")
	}

	copied, removed, err := e.reconcile()
	if err == nil {
		t.Fatal("reconcile completed on a destination that could not be listed")
	}
	if copied != 0 || removed != 0 {
		t.Errorf("a failed pass copied %d and versioned away %d; it must do neither", copied, removed)
	}
}

// A mirror root that does not exist yet is a first backup, not a failure. The
// honest index for it is empty, and everything gets copied.
func TestAMirrorRootThatDoesNotExistYetIsNotAnError(t *testing.T) {
	src := t.TempDir()
	if err := os.WriteFile(filepath.Join(src, "a.txt"), []byte("hi"), 0o644); err != nil {
		t.Fatal(err)
	}
	e := newTestEngine(t, src, NewLocalFS(t.TempDir()), 0)

	idx, err := e.buildDestIndex()
	if err != nil {
		t.Fatalf("first backup treated as an error: %v", err)
	}
	if len(idx.byRel) != 0 {
		t.Errorf("index of a destination that does not exist has %d entries", len(idx.byRel))
	}
	copied, _, err := e.reconcile()
	if err != nil {
		t.Fatal(err)
	}
	if copied != 1 {
		t.Errorf("copied %d files on a first backup, want 1", copied)
	}
}

// THE GUARANTEE: a case-insensitive destination is not recopied for ever.
//
// backend.Stat was case-insensitive on SMB and macOS: asking for "Foo.txt" on a
// server holding "foo.txt" found it. An exact-match map alone would miss, and
// the file would be recopied on every single pass — the forever-recopy failure
// mtime calibration exists to prevent, wearing a different hat.
func TestACaseInsensitiveDestinationIsNotRecopiedForEver(t *testing.T) {
	idx := &destIndex{byRel: map[string]destEntry{
		"Docs/foo.txt": {size: 5, mtime: time.Unix(1000, 0)},
	}}

	got, ok := idx.lookup("Docs/Foo.txt")
	if !ok {
		t.Fatal("a destination holding foo.txt did not answer for Foo.txt, so it would be recopied every pass")
	}
	if got.size != 5 {
		t.Errorf("folded lookup returned %+v, want the entry it matched", got)
	}
}

// The other half: on a case-SENSITIVE destination two files differing only in
// case are two different files, and answering for one with the other's size
// would compare a file against something that is not it.
func TestTwoDestinationFilesDifferingOnlyInCaseAreNeverConfused(t *testing.T) {
	idx := &destIndex{byRel: map[string]destEntry{
		"foo.txt": {size: 5, mtime: time.Unix(1000, 0)},
		"Foo.txt": {size: 99, mtime: time.Unix(2000, 0)},
	}}

	// Exact matches still work.
	if got, ok := idx.lookup("foo.txt"); !ok || got.size != 5 {
		t.Errorf("exact lookup of foo.txt = %+v, %v", got, ok)
	}
	if got, ok := idx.lookup("Foo.txt"); !ok || got.size != 99 {
		t.Errorf("exact lookup of Foo.txt = %+v, %v", got, ok)
	}
	// An inexact one must not guess between them: the safe answer is "copy it".
	if _, ok := idx.lookup("FOO.txt"); ok {
		t.Error("an ambiguous folded key was answered rather than left to be recopied")
	}
}

// THE GUARANTEE: building the index writes nothing to the destination.
//
// It runs before the copy loop, on a path that used to be a plain read. The
// cleanup walk at the end of the pass is the only thing entitled to remove
// anything, under its own guards.
func TestBuildingTheIndexWritesNothingToTheDestination(t *testing.T) {
	_, dstRoot, e, _ := mixedTree(t)
	e.backend = &readOnlyFS{t: t, Backend: NewLocalFS(dstRoot)}

	if _, err := e.buildDestIndex(); err != nil {
		t.Fatal(err)
	}
}

// readOnlyFS fails the test if anything tries to change the destination.
type readOnlyFS struct {
	Backend
	t *testing.T
}

func (r *readOnlyFS) MkdirAll(p string) error {
	r.t.Fatalf("index build called MkdirAll(%q)", p)
	return nil
}

func (r *readOnlyFS) Remove(p string) error {
	r.t.Fatalf("index build called Remove(%q)", p)
	return nil
}

func (r *readOnlyFS) Rename(from, to string) error {
	r.t.Fatalf("index build called Rename(%q, %q)", from, to)
	return nil
}

func (r *readOnlyFS) OpenWrite(p string) (WFile, error) {
	r.t.Fatalf("index build called OpenWrite(%q)", p)
	return nil, nil
}

// THE GUARANTEE: listing the destination several directories at once produces
// exactly the index one directory at a time produces.
//
// The parallel walk exists only to spend the same round trips concurrently.
// If it ever indexed a different set, the difference would show up as files
// silently not copied — which is the failure this whole program exists to
// prevent, arriving through the door marked "performance".
func TestListingTheDestinationInParallelIndexesExactlyTheSameFiles(t *testing.T) {
	_, dstRoot, e, _ := mixedTree(t)

	// A few more levels, so the level-by-level walk has real depth to cover.
	deep := filepath.Join(dstRoot, filepath.FromSlash(e.destRoot), "x/y/z/w")
	if err := os.MkdirAll(deep, 0o755); err != nil {
		t.Fatal(err)
	}
	for i, name := range []string{"one.txt", "two.txt", "three.txt"} {
		if err := os.WriteFile(filepath.Join(deep, name), make([]byte, i+1), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	sequential, err := e.buildDestIndex() // TargetType defaults to "drive"
	if err != nil {
		t.Fatal(err)
	}

	// Through the real backend, not the counting wrapper: embedding the Backend
	// interface promotes only the methods that interface declares, so the
	// wrapper is deliberately not a DirLister.
	dl, ok := NewLocalFS(dstRoot).(DirLister)
	if !ok {
		t.Fatal("the local backend no longer lists directories, so the parallel walk cannot be tested")
	}
	parallel, err := e.buildDestIndexParallel(dl, &destIndex{byRel: map[string]destEntry{}}, 4)
	if err != nil {
		t.Fatal(err)
	}

	if len(sequential.byRel) == 0 {
		t.Fatal("the sequential index is empty, so the comparison proves nothing")
	}
	if len(parallel.byRel) != len(sequential.byRel) {
		t.Fatalf("parallel indexed %d files, sequential %d", len(parallel.byRel), len(sequential.byRel))
	}
	for rel, want := range sequential.byRel {
		got, ok := parallel.byRel[rel]
		if !ok {
			t.Errorf("parallel walk missed %q — it would be recopied every pass", rel)
			continue
		}
		if got.size != want.size || !got.mtime.Equal(want.mtime) {
			t.Errorf("%q: parallel has %+v, sequential %+v", rel, got, want)
		}
	}
}

// A listing that fails partway must fail the pass, exactly as the sequential
// walk does. Several workers hit the dropped connection at once; the answer is
// still to abandon the pass rather than call the missing files absent.
func TestAFailedListingInParallelFailsThePass(t *testing.T) {
	_, _, e, _ := mixedTree(t)

	_, err := e.buildDestIndexParallel(failingLister{}, &destIndex{byRel: map[string]destEntry{}}, 4)
	if err == nil {
		t.Fatal("a destination that could not be listed produced an index instead of an error")
	}
}

type failingLister struct{}

func (failingLister) ReadDir(string) ([]fs.DirEntry, error) {
	return nil, errors.New("connection reset by peer")
}
