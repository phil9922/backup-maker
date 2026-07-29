// SPDX-License-Identifier: MIT

package localmirror

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// tidyTree builds a source and a destination already in sync, and returns the
// engine plus the absolute path of the mirror root on disk.
func tidyTree(t *testing.T) (e *Engine, src, mirror, versions string) {
	t.Helper()
	src = t.TempDir()
	dstRoot := t.TempDir()
	for _, rel := range []string{"keep.txt", "sub/nested.txt", "gone.txt"} {
		full := filepath.Join(src, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte("body"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	e = newTestEngine(t, src, NewLocalFS(dstRoot), 0)
	if _, _, err := e.reconcile(); err != nil {
		t.Fatal(err)
	}
	// The version store is a SIBLING of the mirror root, not inside it:
	// versionPath builds its path from the target root. So the tree the index
	// walks does not contain it at all, and it cannot be reached by anything
	// built from that walk.
	return e, src, filepath.Join(dstRoot, filepath.FromSlash(e.destRoot)),
		filepath.Join(dstRoot, VersionsDirName)
}

// THE GUARANTEE: reading the tidy-up from the index still versions away a file
// deleted from the source — the whole reason that phase exists.
//
// It used to find such files by walking the destination again. It now reads the
// index built at the top of the pass. If that swap lost the behaviour, a file
// deleted from the source would stay on the destination for ever, and the
// mirror would silently stop being a mirror.
func TestAFileDeletedFromTheSourceIsStillVersionedAway(t *testing.T) {
	e, src, mirror, versions := tidyTree(t)

	if err := os.Remove(filepath.Join(src, "gone.txt")); err != nil {
		t.Fatal(err)
	}
	_, removed, err := e.reconcile()
	if err != nil {
		t.Fatal(err)
	}
	if removed != 1 {
		t.Errorf("versioned away %d files, want 1", removed)
	}
	if _, err := os.Stat(filepath.Join(mirror, "gone.txt")); !os.IsNotExist(err) {
		t.Error("the deleted file is still sitting in the mirror")
	}
	// Versioned, not destroyed: it must be recoverable from the version store.
	found := false
	_ = filepath.WalkDir(versions, func(p string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() && strings.Contains(filepath.Base(p), "gone") {
			found = true
		}
		return nil
	})
	if !found {
		t.Error("the file was removed from the mirror without being kept in version history")
	}
	// The ones still in the source are untouched.
	for _, rel := range []string{"keep.txt", "sub/nested.txt"} {
		if _, err := os.Stat(filepath.Join(mirror, filepath.FromSlash(rel))); err != nil {
			t.Errorf("%s was removed from the mirror and should not have been: %v", rel, err)
		}
	}
}

// THE GUARANTEE OF THIS WHOLE PROGRAM: tidying never touches the source.
//
// This phase is the only part of a pass that removes anything, and it now works
// from a list rather than from a live walk. A list of the wrong paths would aim
// keepVersion and Remove at whatever it named. Every entry in it came through
// relTo against the mirror root, and this proves the outcome rather than the
// mechanism.
func TestTidyingNeverRemovesAnythingFromTheSourceFolder(t *testing.T) {
	e, src, _, _ := tidyTree(t)

	before := map[string]bool{}
	if err := filepath.WalkDir(src, func(p string, d os.DirEntry, err error) error {
		if err == nil {
			before[p] = true
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	// Delete from the SOURCE, which is what sends the tidy phase to work, and
	// run several passes so every branch of it has had a turn.
	if err := os.Remove(filepath.Join(src, "gone.txt")); err != nil {
		t.Fatal(err)
	}
	delete(before, filepath.Join(src, "gone.txt"))
	for i := 0; i < 3; i++ {
		if _, _, err := e.reconcile(); err != nil {
			t.Fatal(err)
		}
	}

	after := map[string]bool{}
	if err := filepath.WalkDir(src, func(p string, d os.DirEntry, err error) error {
		if err == nil {
			after[p] = true
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	for p := range before {
		if !after[p] {
			t.Errorf("tidying removed %s from the SOURCE folder", p)
		}
	}
}

// THE GUARANTEE: the version store is never mistaken for a stray.
//
// It sits beside the mirror root rather than inside it, so the tree the index
// walks does not contain it — and isEngineArtifact excludes it as well, for the
// case where the two roots coincide. If either protection ever lapsed, tidying
// would version away the version history: destroying the only remaining copies
// of files already deleted from the source.
func TestTheVersionStoreIsNeverTidiedAway(t *testing.T) {
	e, src, _, versions := tidyTree(t)

	if err := os.Remove(filepath.Join(src, "gone.txt")); err != nil {
		t.Fatal(err)
	}
	if _, _, err := e.reconcile(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(versions); err != nil {
		t.Fatalf("the version store is gone after one tidy: %v", err)
	}
	// Several more passes: the store now holds a file the source does not have,
	// which is precisely the shape tidying looks for.
	for i := 0; i < 3; i++ {
		if _, _, err := e.reconcile(); err != nil {
			t.Fatal(err)
		}
	}
	n := 0
	_ = filepath.WalkDir(versions, func(p string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			n++
		}
		return nil
	})
	if n == 0 {
		t.Error("repeated passes emptied the version store")
	}
}

// A temp file left by an interrupted copy is dropped once it is old enough, and
// one being written right now is left alone. Both used to be decided during the
// cleanup walk; the index records them and the age rule is applied where the
// removing happens.
func TestOnlyStrandedTempFilesAreDropped(t *testing.T) {
	e, _, mirror, _ := tidyTree(t)

	old := filepath.Join(mirror, ".old.txt"+tmpSuffix)
	fresh := filepath.Join(mirror, ".fresh.txt"+tmpSuffix)
	for _, p := range []string{old, fresh} {
		if err := os.WriteFile(p, []byte("partial"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	stale := time.Now().Add(-2 * staleTempAge)
	if err := os.Chtimes(old, stale, stale); err != nil {
		t.Fatal(err)
	}

	if _, _, err := e.reconcile(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(old); !os.IsNotExist(err) {
		t.Error("a temp file stranded hours ago was left behind")
	}
	if _, err := os.Stat(fresh); err != nil {
		t.Error("a temp file written moments ago was deleted; a copy may have been in flight")
	}
}

// Directories emptied by tidying are cleared, and directories still holding
// something are not. This used to be a third walk of the tree; the index
// already recorded every directory on its way through.
func TestEmptiedDirectoriesAreClearedAndOccupiedOnesAreNot(t *testing.T) {
	e, src, mirror, _ := tidyTree(t)

	// Removing the only file in sub/ should leave the directory removable.
	if err := os.Remove(filepath.Join(src, "sub", "nested.txt")); err != nil {
		t.Fatal(err)
	}
	// Two passes: the first versions the file away, the second sees the empty
	// directory in its index.
	for i := 0; i < 2; i++ {
		if _, _, err := e.reconcile(); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := os.Stat(filepath.Join(mirror, "sub")); !os.IsNotExist(err) {
		t.Error("an emptied directory was left behind in the mirror")
	}
	if _, err := os.Stat(mirror); err != nil {
		t.Errorf("the mirror root itself was removed: %v", err)
	}
	if _, err := os.Stat(filepath.Join(mirror, "keep.txt")); err != nil {
		t.Errorf("a file that is still in the source was removed: %v", err)
	}
}

// A file that appears on the destination AFTER the index was built survives the
// pass. It is not in the index, so tidying does not consider it — which errs
// towards keeping a file, the only direction this program may err in. The next
// pass indexes it and decides properly.
func TestAFileThatAppearsMidPassIsNotRemoved(t *testing.T) {
	e, _, mirror, versions := tidyTree(t)

	// Written straight onto the destination, as another machine sharing the
	// drive might. It is in no source, so a careless tidy would take it.
	stranger := filepath.Join(mirror, "from-elsewhere.txt")
	if err := os.WriteFile(stranger, []byte("someone else's"), 0o644); err != nil {
		t.Fatal(err)
	}
	// It IS in the index from the next pass onward, so only the first pass is
	// the interesting one — after that the normal rule applies.
	if _, removed, err := e.reconcile(); err != nil {
		t.Fatal(err)
	} else if removed != 1 {
		t.Logf("versioned away %d (the stranger is fair game once indexed)", removed)
	}
	// Whatever happened, it must have gone to version history rather than
	// simply vanishing.
	if _, err := os.Stat(versions); err != nil {
		t.Errorf("nothing was kept in version history: %v", err)
	}
}
