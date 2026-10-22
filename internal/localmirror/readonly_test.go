// SPDX-License-Identifier: MIT

package localmirror

import (
	"fmt"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

// lockableFS refuses every write while locked, the way a disk does once the
// kernel has remounted it read-only.
type lockableFS struct {
	Backend
	locked atomic.Bool
}

func (l *lockableFS) refuse() error {
	return fmt.Errorf("%w: {Write Protect Error}", ErrReadOnly)
}

func (l *lockableFS) OpenWrite(p string) (WFile, error) {
	if l.locked.Load() {
		return nil, l.refuse()
	}
	return l.Backend.OpenWrite(p)
}

func (l *lockableFS) Rename(o, n string) error {
	if l.locked.Load() {
		return l.refuse()
	}
	return l.Backend.Rename(o, n)
}

func (l *lockableFS) MkdirAll(p string) error {
	if l.locked.Load() {
		return l.refuse()
	}
	return l.Backend.MkdirAll(p)
}

// THE GUARANTEE: a destination that will not take writes is never shown as
// backed up — not after a pass that tried to copy, not after one with nothing
// to copy, and not after a save that went down the fast path — and the clock
// that says when it last received a backup does not move while it refuses.
func TestAReadOnlyDestinationIsNeverReportedAsBackedUp(t *testing.T) {
	src, dst := t.TempDir(), t.TempDir()
	if err := os.WriteFile(filepath.Join(src, "a.txt"), []byte("hi"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := WriteMarkerAt(dst, "uuid-1", "workstation"); err != nil {
		t.Fatal(err)
	}
	b := &lockableFS{Backend: NewLocalFS(dst)}
	e := New(Options{
		FolderID: "f1", TargetName: "dest", SourcePath: src,
		Backend: b, MachineName: "workstation", Label: "code",
		UUID: "uuid-1", MaxAgeDays: 30, Log: quietLog(),
	})

	e.sync()
	if got := e.Status().State; got != "in sync" {
		t.Fatalf("healthy first pass left state %q", got)
	}
	healthy := e.Status().LastSync

	b.locked.Store(true)
	// Nothing new to copy, so no copy will fail: only asking the destination
	// can tell this pass that it would refuse the next change.
	e.sync()
	if got := e.Status().State; got != "read-only" {
		t.Fatalf("an idle pass against a read-only destination left state %q, want read-only", got)
	}

	if err := os.WriteFile(filepath.Join(src, "b.txt"), []byte("new"), 0o644); err != nil {
		t.Fatal(err)
	}
	e.sync()
	if got := e.Status().State; got != "read-only" {
		t.Fatalf("a pass against a read-only destination left state %q, want read-only", got)
	}

	// A save that takes the fast path.
	if err := os.WriteFile(filepath.Join(src, "c.txt"), []byte("saved"), 0o644); err != nil {
		t.Fatal(err)
	}
	e.propagate([]string{"c.txt"})
	if got := e.Status().State; got != "read-only" {
		t.Fatalf("the fast path turned a read-only destination into %q", got)
	}
	// And one that ends up writing nothing at all — the watcher fires for a
	// file deleted again before it was copied. Having attempted nothing, the
	// fast path has learned nothing that could make this destination healthy.
	e.propagate([]string{"deleted-before-copy.txt"})
	if got := e.Status().State; got != "read-only" {
		t.Fatalf("a fast-path pass that wrote nothing turned a read-only destination into %q", got)
	}
	if got := e.Status().LastSync; !got.Equal(healthy) {
		t.Errorf("last sync moved from %v to %v while every write was refused", healthy, got)
	}
	if n := len(e.Status().FileErrors); n != 0 {
		t.Errorf("the destination's refusal was charged to %d individual files", n)
	}

	// Repaired: the next pass sees it, with or without anything changed.
	b.locked.Store(false)
	time.Sleep(10 * time.Millisecond)
	e.sync()
	if got := e.Status().State; got != "in sync" {
		t.Fatalf("after the destination was repaired the state is %q", got)
	}
	for _, f := range []string{"b.txt", "c.txt"} {
		if !exists(filepath.Join(dst, "workstation", "code", f)) {
			t.Errorf("%s was not copied once the destination accepted writes", f)
		}
	}
}
