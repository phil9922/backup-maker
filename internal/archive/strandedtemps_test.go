// SPDX-License-Identifier: MIT

package archive

import (
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/phil9922/backup-maker/internal/localmirror"
)

// tempsIn lists the abandoned .bmtmp files under a destination.
func tempsIn(t *testing.T, dst string) []string {
	t.Helper()
	var out []string
	err := filepath.WalkDir(dst, func(p string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() && strings.HasSuffix(p, ".bmtmp") {
			out = append(out, p)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// THE GUARANTEE: the leftovers of snapshots that never finished cannot pile up.
//
// THE BUG. Stranded temps were swept by prune(), which runs at the END of a
// successful run — so the one failure mode that produces them, a run that never
// finishes, was also the one that never reached the sweep. Every daemon restart
// abandoned another part-written zip. Three reached 8.4GB on an SD card with not
// one completed snapshot to show for it, and two more totalling 2.6GB turned up
// on the Pi the next day. A destination filling with the debris of a snapshot
// that cannot complete is the worst possible use of the space.
//
// Sweeping before the write bounds it at one, however many runs fail.
func TestAbandonedSnapshotTempsDoNotPileUp(t *testing.T) {
	cfg, job, b, dst := testSetup(t)
	dir := filepath.Join(dst, DirName, "mach", "testjob")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	// Three runs that died mid-write, as a restart would leave them.
	for _, stamp := range []string{"20260727-010000", "20260728-010000", "20260729-010000"} {
		p := filepath.Join(dir, "testjob-"+stamp+".zip.bmtmp")
		if err := os.WriteFile(p, make([]byte, 4096), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if got := len(tempsIn(t, dst)); got != 3 {
		t.Fatalf("fixture: %d temps, want 3", got)
	}

	res := Run(b, cfg, job, "hunter2", slog.New(slog.DiscardHandler), nil)
	if res.Err != "" {
		t.Fatalf("run failed: %s", res.Err)
	}
	if got := tempsIn(t, dst); len(got) != 0 {
		t.Errorf("%d abandoned temps survived a completed run: %v", len(got), got)
	}
}

// The half that matters more, because it is the case prune() could never reach:
// a run that FAILS must still have cleared what earlier failures left. Otherwise
// a machine whose snapshots never succeed accumulates for ever, which is exactly
// what happened.
func TestAFailingRunStillClearsWhatEarlierFailuresLeft(t *testing.T) {
	cfg, job, b, dst := testSetup(t)
	dir := filepath.Join(dst, DirName, "mach", "testjob")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	old := filepath.Join(dir, "testjob-20260727-010000.zip.bmtmp")
	if err := os.WriteFile(old, make([]byte, 8192), 0o644); err != nil {
		t.Fatal(err)
	}

	// Fail this run where a real one dies: at the write. The sweep happens just
	// before OpenWrite, so refusing the write puts the failure immediately after
	// it — which is the sequence a daemon restart produces.
	res := Run(&refusesToWrite{Backend: b}, cfg, job, "hunter2",
		slog.New(slog.DiscardHandler), nil)
	if res.Err == "" {
		t.Fatal("this run was supposed to fail; the test proves nothing if it succeeded")
	}

	for _, p := range tempsIn(t, dst) {
		if p == old {
			t.Error("a temp left by an earlier failed run survived another failed run: " +
				"a machine whose snapshots never complete would accumulate them for ever")
		}
	}
}

// refusesToWrite is a destination that will not accept a new file — a full disk,
// a share that dropped, a card pulled out mid-run.
type refusesToWrite struct {
	localmirror.Backend
}

func (r *refusesToWrite) OpenWrite(path string) (localmirror.WFile, error) {
	return nil, errors.New("destination refused the write")
}

// And the one that must NOT be swept: the temp this run is currently writing.
// Deleting that would turn every snapshot into a failed one.
func TestTheSweepLeavesTheTempThisRunIsWriting(t *testing.T) {
	cfg, job, b, dst := testSetup(t)
	res := Run(b, cfg, job, "hunter2", slog.New(slog.DiscardHandler), nil)
	if res.Err != "" {
		t.Fatalf("a run with nothing to sweep failed: %s", res.Err)
	}
	// It completed, so its temp became a real zip rather than being removed.
	var zips int
	err := filepath.WalkDir(dst, func(p string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() && strings.HasSuffix(p, ".zip") {
			zips++
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if zips != 1 {
		t.Errorf("%d zips written, want 1 — the sweep may have removed the temp the "+
			"run was writing", zips)
	}
}
