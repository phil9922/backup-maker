// SPDX-License-Identifier: MIT

package localmirror

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// markRecorder captures what an engine reports about its completed passes.
type markRecorder struct {
	marks []PassMark
}

func (r *markRecorder) record(mk PassMark) { r.marks = append(r.marks, mk) }

func markedEngine(t *testing.T, src string, b Backend, seed *bool, prev time.Time) (*Engine, *markRecorder) {
	t.Helper()
	r := &markRecorder{}
	e := New(Options{
		FolderID: "f1", TargetName: "dest", SourcePath: src,
		Backend: b, MachineName: "workstation", Label: "code",
		MaxAgeDays: 30, Reclaimer: NewReclaimer(), Log: quietLog(),
		MtimeTrusted: seed, PrevScanStart: prev, PassCompleted: r.record,
	})
	return e, r
}

// THE GUARANTEE: only a pass that finished records what it learned.
//
// The mark says "everything up to here has been checked". A pass that died
// partway examined an unknown fraction of the tree, and letting it advance that
// clock would tell the next pass that files it never reached were already
// verified.
func TestOnlyACompletedPassRecordsWhatItLearned(t *testing.T) {
	src := t.TempDir()
	if err := os.WriteFile(filepath.Join(src, "a.txt"), []byte("hi"), 0o644); err != nil {
		t.Fatal(err)
	}
	// One good pass first, so the mirror root exists: the index build returns
	// early on a destination that is not there yet, and a pass that never
	// reaches the destination cannot be interrupted by it.
	dst := t.TempDir()
	warmup, _ := markedEngine(t, src, NewLocalFS(dst), nil, time.Time{})
	if _, _, err := warmup.reconcile(); err != nil {
		t.Fatal(err)
	}

	broken := &brokenLister{Backend: NewLocalFS(dst)}
	e, rec := markedEngine(t, src, broken, nil, time.Time{})

	if _, _, err := e.reconcile(); err == nil {
		t.Fatal("setup failed: the pass was supposed to be interrupted")
	}
	if len(rec.marks) != 0 {
		t.Fatalf("an interrupted pass recorded %d marks; it must record none", len(rec.marks))
	}

	// The same engine against a working destination does record one.
	e2, rec2 := markedEngine(t, src, NewLocalFS(t.TempDir()), nil, time.Time{})
	if _, _, err := e2.reconcile(); err != nil {
		t.Fatal(err)
	}
	if len(rec2.marks) != 1 {
		t.Fatalf("a completed pass recorded %d marks, want 1", len(rec2.marks))
	}
}

// brokenLister fails every destination listing, standing in for a share that
// dropped mid-pass.
type brokenLister struct {
	Backend
}

func (b *brokenLister) WalkDir(root string, fn fs.WalkDirFunc) error {
	return errors.New("connection reset by peer")
}

// THE GUARANTEE: the recorded clock is when the pass BEGAN, not when it ended.
//
// A pass over a share takes minutes. Recording its end time would declare every
// file edited while it ran to have been checked, and the next pass would skip
// them — a file silently never backed up, which is the one failure this program
// exists to prevent.
func TestTheRecordedClockIsThePassStartNotItsEnd(t *testing.T) {
	src := t.TempDir()
	if err := os.WriteFile(filepath.Join(src, "a.txt"), []byte("hi"), 0o644); err != nil {
		t.Fatal(err)
	}
	e, rec := markedEngine(t, src, NewLocalFS(t.TempDir()), nil, time.Time{})

	before := time.Now()
	if _, _, err := e.reconcile(); err != nil {
		t.Fatal(err)
	}
	after := time.Now()

	if len(rec.marks) != 1 {
		t.Fatalf("recorded %d marks, want 1", len(rec.marks))
	}
	got := rec.marks[0].PassStart
	if got.Before(before) || got.After(after) {
		t.Errorf("PassStart = %v, want a time inside the pass's own window [%v, %v]", got, before, after)
	}
	// And it agrees with what the engine will compare against next time.
	if !e.prevScanStart.Equal(got) {
		t.Errorf("recorded %v but the engine kept %v; the next pass would compare against a different clock", got, e.prevScanStart)
	}
}

// THE GUARANTEE: a destination that does not preserve timestamps is not
// recopied in full after every restart.
//
// This is what the mark is actually for. Without a seed, prevScanStart is zero
// on start, which for such a destination reads as "every same-size file has
// changed" — so the whole folder is recopied, once per restart, onto the
// slowest class of destination there is. On a machine restarted 54 times in one
// day that is the difference between a backup and a treadmill.
func TestAnUntrustedTimestampTargetIsNotRecopiedInFullAfterARestart(t *testing.T) {
	src := t.TempDir()
	dstRoot := t.TempDir()
	for _, name := range []string{"a.txt", "b.txt", "c.txt"} {
		if err := os.WriteFile(filepath.Join(src, name), []byte("body"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	untrusted := false
	// First run: it does not know the destination yet, so it copies everything.
	first, rec := markedEngine(t, src, NewLocalFS(dstRoot), &untrusted, time.Time{})
	copied, _, err := first.reconcile()
	if err != nil {
		t.Fatal(err)
	}
	if copied != 3 {
		t.Fatalf("first pass copied %d files, want 3", copied)
	}
	if len(rec.marks) != 1 {
		t.Fatalf("first pass recorded %d marks, want 1", len(rec.marks))
	}
	mk := rec.marks[0]

	// Restart WITH the mark: nothing has changed, so nothing may be recopied.
	seeded, _ := markedEngine(t, src, NewLocalFS(dstRoot), &mk.MtimeTrusted, mk.PassStart)
	copied, _, err = seeded.reconcile()
	if err != nil {
		t.Fatal(err)
	}
	if copied != 0 {
		t.Errorf("a seeded restart recopied %d files that had not changed", copied)
	}

	// And the test bites: the same restart WITHOUT the mark recopies the lot.
	unseeded, _ := markedEngine(t, src, NewLocalFS(dstRoot), &untrusted, time.Time{})
	copied, _, err = unseeded.reconcile()
	if err != nil {
		t.Fatal(err)
	}
	if copied != 3 {
		t.Errorf("an unseeded restart recopied %d files, want all 3 — otherwise the seeded case proves nothing", copied)
	}
}

// THE GUARANTEE: the mark never causes a file to be skipped.
//
// A file edited while the daemon was down has an mtime after the last completed
// pass began, so the seeded engine must still copy it. The mark is only ever
// allowed to suppress a redundant recopy.
func TestASeededRestartStillCopiesAFileChangedWhileTheDaemonWasDown(t *testing.T) {
	src := t.TempDir()
	dstRoot := t.TempDir()
	target := filepath.Join(src, "notes.txt")
	if err := os.WriteFile(target, []byte("before"), 0o644); err != nil {
		t.Fatal(err)
	}

	untrusted := false
	first, rec := markedEngine(t, src, NewLocalFS(dstRoot), &untrusted, time.Time{})
	if _, _, err := first.reconcile(); err != nil {
		t.Fatal(err)
	}
	mk := rec.marks[0]

	// Edited after that pass began, while nothing was watching. Same length, so
	// only the clock can catch it.
	if err := os.WriteFile(target, []byte("AFTER!"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(target, time.Now(), mk.PassStart.Add(time.Second)); err != nil {
		t.Fatal(err)
	}

	seeded, _ := markedEngine(t, src, NewLocalFS(dstRoot), &mk.MtimeTrusted, mk.PassStart)
	copied, _, err := seeded.reconcile()
	if err != nil {
		t.Fatal(err)
	}
	if copied != 1 {
		t.Fatalf("a file changed while the daemon was down was copied %d times, want 1 — the mark must never skip", copied)
	}
	got, err := os.ReadFile(filepath.Join(dstRoot, filepath.FromSlash(seeded.destRoot), "notes.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "AFTER!" {
		t.Errorf("destination holds %q, want the edited contents", got)
	}
}

// A seeded calibration is not re-probed: the probe writes a scratch file to the
// destination and costs a round trip, and on a destination known to fail it,
// re-probing means rediscovering the fallback the hard way every time.
func TestASeededCalibrationIsNotReprobed(t *testing.T) {
	untrusted := false
	e, _ := markedEngine(t, t.TempDir(), NewLocalFS(t.TempDir()), &untrusted, time.Time{})

	if !e.calibrated {
		t.Error("a seeded engine is not marked calibrated, so it will probe the destination again")
	}
	if e.mtimeTrusted {
		t.Error("a seeded engine ignored the recorded result and trusts timestamps anyway")
	}

	// No seed means it must still probe.
	fresh, _ := markedEngine(t, t.TempDir(), NewLocalFS(t.TempDir()), nil, time.Time{})
	if fresh.calibrated {
		t.Error("an unseeded engine claims to be calibrated without having probed anything")
	}
}
