// SPDX-License-Identifier: MIT

package localmirror

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// syncClock stands in for the daemon's persisted sync marks.
type syncClock struct {
	mu    sync.Mutex
	at    []time.Time
	calls int
}

func (c *syncClock) record(at time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.at = append(c.at, at)
	c.calls++
}

func (c *syncClock) last() (time.Time, int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.calls == 0 {
		return time.Time{}, 0
	}
	return c.at[len(c.at)-1], c.calls
}

func clockedEngine(t *testing.T, src, dstRoot, uuid string, seed time.Time) (*Engine, *syncClock) {
	t.Helper()
	c := &syncClock{}
	e := New(Options{
		FolderID: "f1", TargetName: "dest", SourcePath: src,
		Backend: NewLocalFS(dstRoot), MachineName: "workstation", Label: "code",
		UUID: uuid, MaxAgeDays: 30, LastSync: seed, Synced: c.record, Log: quietLog(),
	})
	return e, c
}

// The seed is the whole point: an engine built after a restart reports the
// clock it was given, before it has managed a single pass of its own. A
// destination that has been away for weeks must look weeks old immediately,
// not brand new.
func TestSeededLastSyncIsReportedBeforeAnyPass(t *testing.T) {
	seed := time.Now().Add(-30 * 24 * time.Hour)
	e, _ := clockedEngine(t, t.TempDir(), t.TempDir(), "uuid-1", seed)
	if got := e.Status().LastSync; !got.Equal(seed) {
		t.Fatalf("a fresh engine reports last sync %v, want the seeded %v", got, seed)
	}
}

// A completed pass reports its time back, so the caller can persist it — and
// the engine's own view agrees with what it reported.
func TestCompletedSyncReportsItsTime(t *testing.T) {
	src, dst := t.TempDir(), t.TempDir()
	if err := os.WriteFile(filepath.Join(src, "a.txt"), []byte("hi"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := WriteMarkerAt(dst, "uuid-1", "workstation"); err != nil {
		t.Fatal(err)
	}

	before := time.Now()
	e, c := clockedEngine(t, src, dst, "uuid-1", time.Time{})
	e.sync()

	at, calls := c.last()
	if calls != 1 {
		t.Fatalf("one completed pass reported %d times", calls)
	}
	if at.Before(before) || at.After(time.Now()) {
		t.Errorf("reported sync time %v is not the time of the pass", at)
	}
	if got := e.Status().LastSync; !got.Equal(at) {
		t.Errorf("engine reports %v but told the caller %v", got, at)
	}

	// An idle pass is still a pass: the mirror IS up to date, so the clock moves
	// on. A destination that stopped changing must not drift into looking stale.
	e.sync()
	if _, calls := c.last(); calls != 2 {
		t.Errorf("a pass with nothing to copy reported %d times in total, want 2", calls)
	}
}

// A pass that found nothing to write to synced nothing. Stamping a fresh clock
// there would claim a backup that never happened — and would reset the very
// clock that makes a long-absent destination read as stale.
func TestAnAbsentTargetNeitherReportsNorClearsTheSeed(t *testing.T) {
	seed := time.Now().Add(-30 * 24 * time.Hour)
	src := t.TempDir()
	e, c := clockedEngine(t, src, filepath.Join(t.TempDir(), "unplugged"), "uuid-1", seed)

	e.sync()
	if st := e.Status(); st.State != "offline" {
		t.Fatalf("state = %q, want offline", st.State)
	}
	if _, calls := c.last(); calls != 0 {
		t.Errorf("an absent target reported %d syncs", calls)
	}
	if got := e.Status().LastSync; !got.Equal(seed) {
		t.Errorf("an absent target reset the clock to %v, losing the seeded %v", got, seed)
	}
}

// An engine built without the callback (every other test in this package, and
// any caller that doesn't persist the clock) must not panic on a nil seam.
func TestSyncingWithoutASyncedCallbackIsHarmless(t *testing.T) {
	dst := t.TempDir()
	if err := WriteMarkerAt(dst, "uuid-1", "workstation"); err != nil {
		t.Fatal(err)
	}
	e := New(Options{
		FolderID: "f1", TargetName: "dest", SourcePath: t.TempDir(),
		Backend: NewLocalFS(dst), MachineName: "workstation", Label: "code",
		UUID: "uuid-1", MaxAgeDays: 30, Log: quietLog(),
	})
	e.sync()
	if st := e.Status(); st.State != "in sync" {
		t.Fatalf("state = %q, want in sync", st.State)
	}
}
