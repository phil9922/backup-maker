// SPDX-License-Identifier: MIT

package localmirror

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// countingBackend records the calls that cost a round trip, so a test can prove
// the fast path did NOT enumerate the destination.
type countingBackend struct {
	Backend
	walks    int
	readDirs int
	removes  []string
	renames  [][2]string
}

func (c *countingBackend) WalkDir(root string, fn fs.WalkDirFunc) error {
	c.walks++
	return c.Backend.WalkDir(root, fn)
}

func (c *countingBackend) ReadDir(p string) ([]fs.DirEntry, error) {
	c.readDirs++
	if dl, ok := c.Backend.(DirLister); ok {
		return dl.ReadDir(p)
	}
	return nil, fs.ErrInvalid
}

func (c *countingBackend) Remove(p string) error {
	c.removes = append(c.removes, p)
	return c.Backend.Remove(p)
}

func (c *countingBackend) Rename(from, to string) error {
	c.renames = append(c.renames, [2]string{from, to})
	return c.Backend.Rename(from, to)
}

// fastEngine builds an engine over a real source and destination, with one pass
// already completed so the fast path is allowed to run.
func fastEngine(t *testing.T) (*Engine, string, string, *countingBackend) {
	t.Helper()
	src, dst := t.TempDir(), t.TempDir()
	write(t, src, "a.txt", "alpha")
	write(t, src, "sub/b.txt", "beta")

	cb := &countingBackend{Backend: NewLocalFS(dst)}
	if err := WriteMarker(cb, "uuid-1", "mach"); err != nil {
		t.Fatal(err)
	}
	e := New(Options{
		FolderID: "f1", TargetName: "dest", SourcePath: src, Backend: cb,
		MachineName: "mach", Label: "proj", UUID: "uuid-1", MaxAgeDays: 30,
		Ignores: []string{"node_modules"}, Log: quietLog(),
	})
	e.sync() // the completed pass that makes the fast path legal
	cb.walks, cb.readDirs = 0, 0
	cb.removes, cb.renames = nil, nil
	return e, src, dst, cb
}

func write(t *testing.T, root, rel, body string) {
	t.Helper()
	p := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func mirrored(t *testing.T, dst, rel string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dst, "mach", "proj", filepath.FromSlash(rel)))
	if err != nil {
		return ""
	}
	return string(b)
}

// THE GUARANTEE: a changed file reaches the destination without the destination
// being enumerated first.
//
// That enumeration is 26 seconds of a 27.5-second pass on a real share, and all of
// it sat between somebody pressing save and their file existing anywhere else — the
// one number the front page makes a promise about.
func TestAChangedFileIsCopiedWithoutListingTheDestination(t *testing.T) {
	e, src, dst, cb := fastEngine(t)
	write(t, src, "a.txt", "alpha edited")

	if n := e.propagate([]string{"a.txt"}); n != 1 {
		t.Fatalf("propagate copied %d files, want 1", n)
	}
	if got := mirrored(t, dst, "a.txt"); got != "alpha edited" {
		t.Errorf("destination has %q, want %q", got, "alpha edited")
	}
	if cb.walks != 0 || cb.readDirs != 0 {
		t.Errorf("the fast path enumerated the destination: %d walks, %d readdirs — "+
			"that is the cost it exists to avoid", cb.walks, cb.readDirs)
	}
}

// THE GUARANTEE THAT MATTERS MOST: the fast path never removes or versions away.
//
// Versioning away is a decision the safety guards make against a COMPLETE index of
// the destination. This path has no such index, so it must not make that decision at
// all — not even for a file the watcher plainly saw being deleted. Late is fine; the
// full pass handles it within the hour. Wrong is invisible and irreversible.
func TestTheFastPathNeverRemovesAnythingEvenAFileDeletedFromTheSource(t *testing.T) {
	e, src, dst, cb := fastEngine(t)
	if mirrored(t, dst, "sub/b.txt") != "beta" {
		t.Fatal("fixture: sub/b.txt was not mirrored by the first pass")
	}

	// Delete it from the source and hand the fast path exactly that path, which is
	// what the watcher would do.
	if err := os.Remove(filepath.Join(src, "sub", "b.txt")); err != nil {
		t.Fatal(err)
	}
	e.propagate([]string{"sub/b.txt"})

	if got := mirrored(t, dst, "sub/b.txt"); got != "beta" {
		t.Error("the fast path removed or versioned away a file deleted from the " +
			"source; only the full pass may make that decision")
	}
	if len(cb.removes) != 0 {
		t.Errorf("the fast path called Remove: %v", cb.removes)
	}
	for _, r := range cb.renames {
		if !strings.Contains(r[1], tmpSuffix) && !strings.Contains(r[0], tmpSuffix) {
			t.Errorf("the fast path renamed %q → %q, which is how a file gets "+
				"versioned away", r[0], r[1])
		}
	}

	// And the full pass still does its job afterwards.
	e.sync()
	if got := mirrored(t, dst, "sub/b.txt"); got != "" {
		t.Error("the following full pass did not version away the deleted file")
	}
}

// Overwriting keeps the previous copy, because the fast path shares copyFile with
// the full pass rather than reimplementing it. Losing version history quietly would
// be the subtlest way for this path to cost somebody something.
func TestTheFastPathKeepsThePreviousVersionWhenItOverwrites(t *testing.T) {
	e, src, dst, _ := fastEngine(t)
	write(t, src, "a.txt", "second draft")
	if n := e.propagate([]string{"a.txt"}); n != 1 {
		t.Fatalf("copied %d, want 1", n)
	}

	var found bool
	err := filepath.WalkDir(dst, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		if b, rerr := os.ReadFile(p); rerr == nil && string(b) == "alpha" &&
			strings.Contains(p, "versions") {
			found = true
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if !found {
		t.Error("the overwritten copy was not kept in the version store")
	}
}

// Excluded paths are excluded here too. The fast path applies the same rules the
// full pass's source walk applies; if it did not, an excluded file could reach the
// destination by the back door and then be versioned away by the next full pass —
// churn with no explanation.
func TestTheFastPathHonoursExclusions(t *testing.T) {
	e, src, dst, _ := fastEngine(t)
	write(t, src, "node_modules/junk.js", "junk")

	if n := e.propagate([]string{"node_modules/junk.js"}); n != 0 {
		t.Errorf("copied %d excluded files, want 0", n)
	}
	if got := mirrored(t, dst, "node_modules/junk.js"); got != "" {
		t.Error("an excluded file was copied by the fast path")
	}
}

// THE GUARANTEE: the fast path does not claim the tree has been checked.
//
// A PassMark means "everything up to here has been verified". This verified a
// handful of files. Recording one would let the next full pass skip work on the
// strength of a promise nobody made.
func TestTheFastPathDoesNotRecordACompletedPass(t *testing.T) {
	src, dst := t.TempDir(), t.TempDir()
	write(t, src, "a.txt", "alpha")
	b := NewLocalFS(dst)
	if err := WriteMarker(b, "uuid-1", "mach"); err != nil {
		t.Fatal(err)
	}
	rec := &markRecorder{}
	e := New(Options{
		FolderID: "f1", TargetName: "dest", SourcePath: src, Backend: b,
		MachineName: "mach", Label: "proj", UUID: "uuid-1", MaxAgeDays: 30,
		Log: quietLog(), PassCompleted: rec.record,
	})
	e.sync()
	marks := len(rec.marks)
	if marks == 0 {
		t.Fatal("fixture: the first full pass recorded no mark")
	}

	write(t, src, "a.txt", "alpha edited")
	e.propagate([]string{"a.txt"})
	if len(rec.marks) != marks {
		t.Errorf("the fast path recorded a PassMark (%d → %d): it checked one file, "+
			"not the tree", marks, len(rec.marks))
	}
}

// A burst is not somebody saving, and a folder with no completed pass has nothing
// to compare against. Both fall back to the full pass.
func TestTheFastPathStandsAsideForBurstsAndFirstBackups(t *testing.T) {
	e, _, _, _ := fastEngine(t)

	big := make([]string, maxFastPathFiles+1)
	for i := range big {
		big[i] = "f.txt"
	}
	if e.fastPathUsable(big) {
		t.Errorf("a batch of %d took the fast path; over %d belongs to the full pass",
			len(big), maxFastPathFiles)
	}
	if e.fastPathUsable(nil) {
		t.Error("an empty batch took the fast path")
	}

	// A folder that has never completed a pass.
	fresh := New(Options{
		FolderID: "f2", TargetName: "dest", SourcePath: t.TempDir(),
		Backend: NewLocalFS(t.TempDir()), MachineName: "mach", Label: "proj",
		MaxAgeDays: 30, Log: quietLog(),
	})
	if fresh.fastPathUsable([]string{"a.txt"}) {
		t.Error("a folder with no completed pass took the fast path; a first backup " +
			"has nothing on the destination to compare against")
	}
}

// The write gates are the same ones the full pass uses, so storage that is not ours
// is refused here too. This is the failure that would matter most: writing somebody
// else's files because a second code path forgot to ask.
func TestTheFastPathRefusesStorageThatIsNotOurs(t *testing.T) {
	e, src, dst, _ := fastEngine(t)
	write(t, src, "a.txt", "alpha edited")

	// Replace the marker with another machine's, which is what a reformatted or
	// foreign destination looks like.
	if err := WriteMarker(e.backend, "someone-elses-uuid", "other-machine"); err != nil {
		t.Fatal(err)
	}
	if n := e.propagate([]string{"a.txt"}); n != 0 {
		t.Errorf("the fast path wrote %d files to storage it does not own", n)
	}
	if got := mirrored(t, dst, "a.txt"); got == "alpha edited" {
		t.Error("the fast path wrote to a destination whose marker is not ours")
	}
	if st := e.Status(); st.State != "wrong-drive" {
		t.Errorf("state = %q after refusing, want %q", st.State, "wrong-drive")
	}
	_ = time.Now
}
