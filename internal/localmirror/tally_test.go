// SPDX-License-Identifier: MIT

package localmirror

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// countingEngine builds an engine wired to a counter, returning both.
func countingEngine(t *testing.T, src string, b Backend) (*Engine, *counter) {
	t.Helper()
	c := &counter{}
	e := New(Options{
		FolderID:    "f1",
		TargetName:  "dest",
		SourcePath:  src,
		Backend:     b,
		MachineName: "workstation",
		Label:       "code",
		MaxAgeDays:  30,
		Reclaimer:   NewReclaimer(),
		Counted:     c.add,
		Log:         quietLog(),
	})
	return e, c
}

// counter stands in for the daemon's odometer.
type counter struct {
	mu    sync.Mutex
	bytes int64
	files int
}

func (c *counter) add(n int64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.bytes += n
	c.files++
}

func (c *counter) read() (int64, int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.bytes, c.files
}

// The odometer is a record of work done, not of what is currently protected: a
// file that changes and is copied again counts twice, and a pass with nothing
// to do adds nothing at all.
func TestCountsEveryCompletedCopyIncludingRecopies(t *testing.T) {
	src := t.TempDir()
	b := NewLocalFS(t.TempDir())
	if err := os.WriteFile(filepath.Join(src, "a.bin"), make([]byte, 4000), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "b.bin"), make([]byte, 1000), 0o644); err != nil {
		t.Fatal(err)
	}

	e, c := countingEngine(t, src, b)
	if _, _, err := e.reconcile(); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if bytes, files := c.read(); bytes != 5000 || files != 2 {
		t.Fatalf("first pass counted %dB across %d files, want 5000B across 2", bytes, files)
	}

	// Nothing changed: a second pass copies nothing, so the odometer must not
	// move. A counter that ticked on every scan would invent terabytes.
	if _, _, err := e.reconcile(); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if bytes, files := c.read(); bytes != 5000 || files != 2 {
		t.Fatalf("an idle pass moved the odometer to %dB / %d files", bytes, files)
	}

	// One file changes and is copied again. That IS more work done, so it
	// counts again — this is an odometer, not a gauge of what is protected.
	if err := os.WriteFile(filepath.Join(src, "b.bin"), make([]byte, 1500), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := e.reconcile(); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if bytes, files := c.read(); bytes != 6500 || files != 3 {
		t.Fatalf("after a re-copy the odometer reads %dB / %d files, want 6500B / 3", bytes, files)
	}
}

// failRenameFS fails the final rename for one file, so a copy dies after every
// byte has been written but before anything lands at the destination.
type failRenameFS struct {
	Backend
	fail string
}

func (f *failRenameFS) Rename(from, to string) error {
	if strings.HasSuffix(to, f.fail) {
		return errors.New("destination went away")
	}
	return f.Backend.Rename(from, to)
}

// THE RULE THIS GUARDS: bytes that did not survive were not backed up. A copy
// that fails leaves nothing at the destination, so counting its bytes would
// inflate the one figure a user is invited to trust.
func TestFailedCopyIsNotCounted(t *testing.T) {
	src := t.TempDir()
	if err := os.WriteFile(filepath.Join(src, "good.bin"), make([]byte, 900), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "doomed.bin"), make([]byte, 7000), 0o644); err != nil {
		t.Fatal(err)
	}

	b := &failRenameFS{Backend: NewLocalFS(t.TempDir()), fail: "doomed.bin"}
	e, c := countingEngine(t, src, b)
	if _, _, err := e.reconcile(); err != nil {
		t.Fatalf("reconcile: %v", err)
	}

	if bytes, files := c.read(); bytes != 900 || files != 1 {
		t.Fatalf("odometer reads %dB across %d files; the failed copy's 7000B were counted", bytes, files)
	}
	// The failure itself is still reported — this test is about the counter,
	// not about swallowing errors.
	if len(e.Status().FileErrors) == 0 {
		t.Error("the failed copy was not recorded as a file error")
	}
}

// A destination that fills up mid-pass stops the pass. Whatever landed before
// that counts; the file that ran out of room does not.
func TestOutOfSpaceCopyIsNotCounted(t *testing.T) {
	src := t.TempDir()
	dstRoot := t.TempDir()
	if err := os.WriteFile(filepath.Join(src, "a.bin"), make([]byte, 2000), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "b.bin"), make([]byte, 9000), 0o644); err != nil {
		t.Fatal(err)
	}

	// Room for a.bin and nothing else, and no history to reclaim.
	e, c := countingEngine(t, src, newQuotaFS(dstRoot, 3000))
	if _, _, err := e.reconcile(); !IsNoSpace(err) {
		t.Fatalf("reconcile err = %v, want an out-of-space error", err)
	}
	if bytes, files := c.read(); bytes != 2000 || files != 1 {
		t.Fatalf("odometer reads %dB across %d files, want only a.bin's 2000B", bytes, files)
	}
}

// An engine built without a counter (every test in this package but these, and
// any future caller that doesn't want one) must not panic on a nil callback.
func TestCopyingWithoutACounterIsHarmless(t *testing.T) {
	src := t.TempDir()
	if err := os.WriteFile(filepath.Join(src, "a.bin"), make([]byte, 10), 0o644); err != nil {
		t.Fatal(err)
	}
	e := newTestEngine(t, src, NewLocalFS(t.TempDir()), 0)
	if _, _, err := e.reconcile(); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
}

// copyFile reports what actually reached the destination, which is what the
// odometer records. Zero-length files are real files and count as such.
func TestCopyFileReportsBytesWritten(t *testing.T) {
	src := t.TempDir()
	b := NewLocalFS(t.TempDir())

	big := filepath.Join(src, "big.bin")
	if err := os.WriteFile(big, make([]byte, 3333), 0o644); err != nil {
		t.Fatal(err)
	}
	n, err := copyFile(b, big, "big.bin", time.Now(), false, nil)
	if err != nil || n != 3333 {
		t.Fatalf("copyFile = %d, %v; want 3333 bytes", n, err)
	}

	empty := filepath.Join(src, "empty.bin")
	if err := os.WriteFile(empty, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	n, err = copyFile(b, empty, "empty.bin", time.Now(), false, nil)
	if err != nil || n != 0 {
		t.Fatalf("copyFile(empty) = %d, %v; want 0 bytes and no error", n, err)
	}
}
