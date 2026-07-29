// SPDX-License-Identifier: MIT

package localmirror

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestCompletionReportsIdleAsComplete(t *testing.T) {
	// Nothing pending is the steady state. Reporting 0% there would paint a
	// healthy mirror as though it had never run.
	if got := (Status{}).Completion(); got != 100 {
		t.Errorf("idle Completion() = %v, want 100", got)
	}
}

func TestCompletionByBytes(t *testing.T) {
	s := Status{DoneBytes: 25, TotalBytes: 100, DoneFiles: 1, TotalFiles: 4}
	if got := s.Completion(); got != 25 {
		t.Errorf("Completion() = %v, want 25", got)
	}
}

// Byte totals come from the source tree; if a file grows mid-pass the counters
// can overshoot, and a bar wider than its track looks broken.
func TestCompletionClampsAbove100(t *testing.T) {
	s := Status{DoneBytes: 150, TotalBytes: 100}
	if got := s.Completion(); got != 100 {
		t.Errorf("Completion() = %v, want it clamped to 100", got)
	}
}

// Zero-byte files are real: a pass copying only empty files still has to show
// progress, so fall back to counting files.
func TestCompletionFallsBackToFileCount(t *testing.T) {
	s := Status{DoneFiles: 3, TotalFiles: 4, TotalBytes: 0}
	if got := s.Completion(); got != 75 {
		t.Errorf("Completion() = %v, want 75 from the file count", got)
	}
}

func TestTransferCountersLifecycle(t *testing.T) {
	e := &Engine{}

	e.beginTransfer(2, 300)
	if st := e.Status(); st.TotalFiles != 2 || st.TotalBytes != 300 || st.DoneFiles != 0 {
		t.Fatalf("after begin: %+v", st)
	}

	e.advanceTransfer(100)
	if st := e.Status(); st.DoneFiles != 1 || st.DoneBytes != 100 {
		t.Fatalf("after one file: %+v", st)
	}
	if got := e.Status().Completion(); got < 33 || got > 34 {
		t.Errorf("Completion() = %v, want about 33", got)
	}

	e.advanceTransfer(200)
	if got := e.Status().Completion(); got != 100 {
		t.Errorf("Completion() after all files = %v, want 100", got)
	}

	// Counters must be cleared, or an idle mirror keeps showing the last
	// transfer's numbers forever.
	e.endTransfer()
	st := e.Status()
	if st.TotalFiles != 0 || st.TotalBytes != 0 || st.DoneFiles != 0 || st.DoneBytes != 0 {
		t.Errorf("counters lingered after endTransfer: %+v", st)
	}
}

// A target vanishing mid-pass must not leave done > total, which would render
// as an over-full bar.
func TestTransferCountersSurviveInterruption(t *testing.T) {
	e := &Engine{}
	e.beginTransfer(5, 500)
	e.advanceTransfer(100)
	e.advanceTransfer(100)
	st := e.Status()
	if st.DoneFiles > st.TotalFiles || st.DoneBytes > st.TotalBytes {
		t.Errorf("counters exceeded their totals: %+v", st)
	}
	if got := st.Completion(); got != 40 {
		t.Errorf("Completion() = %v, want 40 for a half-finished pass", got)
	}
}

// hookedFS calls a hook on every chunk written to the destination, so a test
// can observe what the dashboard would see partway through a single file.
type hookedFS struct {
	Backend
	onWrite func(chunk int)
}

type hookedFile struct {
	WFile
	fs *hookedFS
}

func (f *hookedFile) Write(p []byte) (int, error) {
	n, err := f.WFile.Write(p)
	if f.fs.onWrite != nil {
		f.fs.onWrite(n)
	}
	return n, err
}

func (h *hookedFS) OpenWrite(p string) (WFile, error) {
	w, err := h.Backend.OpenWrite(p)
	if err != nil {
		return nil, err
	}
	return &hookedFile{WFile: w, fs: h}, nil
}

// copyFile must report progress as it goes. Counting only completed files
// leaves the bar frozen for the whole of a large file, which is exactly when a
// user most wants to know something is happening.
func TestCopyFileReportsProgressWithinAFile(t *testing.T) {
	src := t.TempDir()
	const size = 1 << 20
	srcFile := filepath.Join(src, "big.bin")
	if err := os.WriteFile(srcFile, make([]byte, size), 0o644); err != nil {
		t.Fatal(err)
	}

	var reports []int64
	b := NewLocalFS(t.TempDir())
	if _, err := copyFile(b, srcFile, "big.bin", time.Now(), false, func(written int64) {
		reports = append(reports, written)
	}); err != nil {
		t.Fatalf("copyFile: %v", err)
	}

	if len(reports) < 3 {
		t.Fatalf("a 1MB file produced %d progress reports; the bar would barely move", len(reports))
	}
	var prev int64 = -1
	for i, r := range reports {
		if r < prev {
			t.Fatalf("progress went backwards mid-file at report %d: %d after %d", i, r, prev)
		}
		prev = r
	}
	if last := reports[len(reports)-1]; last != size {
		t.Errorf("final progress report was %d, want the full %d", last, size)
	}
	// The point of the change: something is reported before the file lands.
	if reports[len(reports)-2] >= size {
		t.Error("every byte was reported at once; progress is still effectively per-file")
	}
}

// A retry re-sends the file from the beginning, so progress must rewind rather
// than continue climbing — otherwise a retried file reports twice its size.
func TestCopyFileProgressRewindsOnRetry(t *testing.T) {
	src := t.TempDir()
	srcFile := filepath.Join(src, "f.bin")
	if err := os.WriteFile(srcFile, make([]byte, 128<<10), 0o644); err != nil {
		t.Fatal(err)
	}

	var reports []int64
	b := &flakyVerifyFS{Backend: NewLocalFS(t.TempDir())}
	if _, err := copyFile(b, srcFile, "f.bin", time.Now(), true, func(written int64) {
		reports = append(reports, written)
	}); err != nil {
		t.Fatalf("copyFile: %v", err)
	}
	if b.reads < 2 {
		t.Fatalf("expected the verification failure to force a retry, got %d verification reads", b.reads)
	}

	rewound := false
	for i := 1; i < len(reports); i++ {
		if reports[i] < reports[i-1] {
			rewound = true
			if reports[i] != 0 {
				t.Errorf("retry resumed progress at %d instead of restarting at 0", reports[i])
			}
		}
	}
	if !rewound {
		t.Error("progress never rewound across a retry, so the retried bytes were counted twice")
	}
}

// flakyVerifyFS fails the first verification read, forcing copyFile's retry.
type flakyVerifyFS struct {
	Backend
	reads int
}

func (f *flakyVerifyFS) OpenRead(p string) (io.ReadCloser, error) {
	f.reads++
	if f.reads == 1 {
		return io.NopCloser(strings.NewReader("not what was written")), nil
	}
	return f.Backend.OpenRead(p)
}

// The user-visible result: Status() advances while a single large file is still
// being copied, instead of jumping from 0 to 100 when it lands.
func TestStatusAdvancesWhileOneLargeFileCopies(t *testing.T) {
	src := t.TempDir()
	const size = 1 << 20
	if err := os.WriteFile(filepath.Join(src, "big.bin"), make([]byte, size), 0o644); err != nil {
		t.Fatal(err)
	}

	var e *Engine
	var midFile []int64
	h := &hookedFS{Backend: NewLocalFS(t.TempDir())}
	h.onWrite = func(int) {
		st := e.Status()
		midFile = append(midFile, st.DoneBytes)
	}
	e = newTestEngine(t, src, h, 0)

	if _, _, err := e.reconcile(); err != nil {
		t.Fatalf("reconcile: %v", err)
	}

	moved := false
	for _, b := range midFile {
		if b > 0 && b < size {
			moved = true
		}
		if b > size {
			t.Fatalf("mid-file progress reported %d bytes done of a %d byte transfer", b, size)
		}
	}
	if !moved {
		t.Error("Status() never showed partial progress during a 1MB single-file transfer")
	}

	// And once the pass is over nothing lingers.
	st := e.Status()
	if st.DoneBytes != 0 || st.TotalBytes != 0 {
		t.Errorf("progress lingered after the pass finished: %+v", st)
	}
}

// When a file lands, its bytes move from the in-flight counter into the
// settled total. If the in-flight figure were left behind, the finished file
// would be counted twice and the bar would run ahead of reality — reading as
// complete while the transfer was still going.
func TestInFlightBytesAreNotCountedTwiceOnceAFileLands(t *testing.T) {
	e := &Engine{}
	e.beginTransfer(3, 300)

	e.reportInFlight(100) // first file, fully written but not yet renamed
	if got := e.Status().DoneBytes; got != 100 {
		t.Errorf("mid-file progress reported %d bytes done, want 100", got)
	}

	e.advanceTransfer(100) // it lands
	if got := e.Status().DoneBytes; got != 100 {
		t.Errorf("after the file landed, progress reported %d bytes done, want 100 — "+
			"its bytes were counted both in flight and as done", got)
	}

	e.reportInFlight(50) // second file, partway
	if got := e.Status().DoneBytes; got != 150 {
		t.Errorf("progress reported %d bytes done, want 150 (one landed file plus half of the next)", got)
	}
}

// THE GUARANTEE: a scan reports how far through it is, not just how far it has
// come — a count with no denominator cannot say whether it is nearly done.
//
// The scan is where the minutes go on a network share: one round trip per file
// to decide what needs copying, before a byte moves. A destination steadily
// working through 70,000 files displayed a full bar, a dash and the word
// "never", which reads as a destination nothing has ever been written to.
func TestAScanReportsHowManyFilesItHasCheckedAndOutOfHowMany(t *testing.T) {
	src := t.TempDir()
	for _, name := range []string{"a.txt", "b.txt", "c.txt"} {
		if err := os.WriteFile(filepath.Join(src, name), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	e := newTestEngine(t, src, NewLocalFS(t.TempDir()), 0)

	// One pass to populate the destination: against an empty one there is
	// nothing to list and nothing to tidy, so those counters would read zero
	// for reasons that say nothing about whether they work.
	if _, _, err := e.reconcile(); err != nil {
		t.Fatal(err)
	}

	// Read after the pass: tidying is the last phase, and it now works from the
	// index rather than a walk of its own, so there is no backend call left to
	// sample at. Its counters are what a finished pass leaves behind.
	if _, _, err := e.reconcile(); err != nil {
		t.Fatal(err)
	}
	st := e.Status()
	if st.Phase != "tidying" {
		t.Fatalf("phase after a pass = %q, want tidying", st.Phase)
	}
	if st.ScanTotalFiles != 3 {
		t.Errorf("ScanTotalFiles = %d, want 3 — the destination's own file count", st.ScanTotalFiles)
	}
	if st.ScannedFiles < 3 {
		t.Errorf("ScannedFiles = %d, want at least the 3 files it checked", st.ScannedFiles)
	}
}

// Before the source walk finishes there is genuinely no denominator, and the
// honest answer is to say so. Inventing one — the previous pass's count, say —
// would draw a bar that jumps backwards when the real number arrives.
func TestAScanClaimsNoDenominatorBeforeItKnowsOne(t *testing.T) {
	e := &Engine{}
	if st := e.Status(); st.ScanTotalFiles != 0 {
		t.Errorf("ScanTotalFiles = %d before any scan, want 0", st.ScanTotalFiles)
	}
}
