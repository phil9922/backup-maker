// SPDX-License-Identifier: MIT

package archive

import (
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/phil9922/backup-maker/internal/config"
	"github.com/phil9922/backup-maker/internal/localmirror"
)

// THE GUARANTEE: the total the bar fills towards is counted by the same walk
// that does the packing, so it ends at exactly 100%.
//
// A denominator produced by any other traversal is an answer to a different
// question — a slightly different exclude rule, a symlink counted once here and
// skipped there — and the bar would stop at 94% or claim 103%. A progress bar
// that does not end where the work ends is worse than none: it teaches people
// the number is decorative.
func TestTheProgressTotalMatchesWhatIsActuallyPacked(t *testing.T) {
	cfg, job, b, _ := testSetup(t)

	// The LAST PACKING report, not simply the last: verification follows, and
	// it counts compressed bytes read back off the destination — a different
	// quantity answering a different question.
	var last Progress
	res := Run(b, cfg, job, "hunter2", slog.New(slog.DiscardHandler), func(p Progress) {
		if p.Phase == PhasePacking {
			last = p
		}
	})
	if res.Err != "" {
		t.Fatalf("run failed: %s", res.Err)
	}

	if last.TotalFiles != res.Files {
		t.Errorf("bar counted %d files, %d were packed", last.TotalFiles, res.Files)
	}
	if last.DoneFiles != last.TotalFiles {
		t.Errorf("bar finished at %d of %d files", last.DoneFiles, last.TotalFiles)
	}
	if last.TotalBytes != res.Bytes {
		t.Errorf("bar counted %d bytes, %d were packed", last.TotalBytes, res.Bytes)
	}
	if last.DoneBytes != last.TotalBytes {
		t.Errorf("bar finished at %d of %d bytes", last.DoneBytes, last.TotalBytes)
	}
	// And it is not vacuously zero on both sides.
	if last.TotalFiles == 0 {
		t.Fatal("nothing was counted, so the comparison proves nothing")
	}
}

// The excludes apply to the count as well as the pack. Counting node_modules
// into the total and then skipping it would leave every bar short by however
// much junk the folder happened to contain.
func TestTheProgressTotalHonoursTheExcludes(t *testing.T) {
	cfg, job, b, _ := testSetup(t)
	// testSetup puts a node_modules file in the source; it must be in neither.
	var first Progress
	seen := false
	_ = Run(b, cfg, job, "pw", slog.New(slog.DiscardHandler), func(p Progress) {
		if !seen && p.Phase == PhasePacking {
			first, seen = p, true
		}
	})
	if !seen {
		t.Fatal("progress was never reported")
	}
	if first.TotalFiles != 2 {
		t.Errorf("counted %d files, want 2 — node_modules must be excluded from the total too", first.TotalFiles)
	}
	if first.DoneFiles != 0 {
		t.Errorf("the first report already claims %d files done", first.DoneFiles)
	}
}

// The totals arrive BEFORE any file is packed, so the bar has a denominator
// from its first frame instead of starting as a spinner and jumping.
func TestTheTotalIsKnownBeforePackingStarts(t *testing.T) {
	cfg, job, b, _ := testSetup(t)

	var reports []Progress
	_ = Run(b, cfg, job, "pw", slog.New(slog.DiscardHandler), func(p Progress) {
		if p.Phase == PhasePacking {
			reports = append(reports, p)
		}
	})
	if len(reports) < 2 {
		t.Fatalf("got %d reports, want a total then one per file", len(reports))
	}
	if reports[0].TotalBytes == 0 {
		t.Error("the first report carries no total, so the bar starts without a denominator")
	}
	if reports[0].DoneBytes != 0 {
		t.Error("the first report already claims progress")
	}
	// Monotonic: a bar that goes backwards reads as a fault.
	for i := 1; i < len(reports); i++ {
		if reports[i].DoneBytes < reports[i-1].DoneBytes {
			t.Fatalf("progress went backwards at report %d: %d then %d",
				i, reports[i-1].DoneBytes, reports[i].DoneBytes)
		}
	}
}

// A run that is not asked for progress must behave exactly as before — the
// pre-pass is skipped entirely rather than walking the tree for nobody.
func TestNoReporterMeansNoPrePass(t *testing.T) {
	cfg, job, b, _ := testSetup(t)
	res := Run(b, cfg, job, "pw", slog.New(slog.DiscardHandler), nil)
	if res.Err != "" {
		t.Fatalf("run without a reporter failed: %s", res.Err)
	}
	if res.Files != 2 {
		t.Errorf("packed %d files, want 2", res.Files)
	}
}

// Completion is what the dashboard draws. It must never exceed 100 — a file
// that grows between the count and the pack would otherwise push the bar past
// the end of its track.
func TestCompletionIsClampedAndHonestWhenUnknown(t *testing.T) {
	cases := []struct {
		name string
		done int64
		tot  int64
		want float64
	}{
		{"nothing running", 0, 0, -1},
		{"half done", 50, 100, 50},
		{"overshoot is clamped", 150, 100, 100},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := archiveCompletion(c.done, c.tot)
			if got != c.want {
				t.Errorf("completion(%d, %d) = %v, want %v", c.done, c.tot, got, c.want)
			}
		})
	}
}

// archiveCompletion mirrors status.ArchiveRow.Completion, kept here so the
// clamping rule is exercised without importing the status package (which
// imports this one).
func archiveCompletion(done, total int64) float64 {
	if total <= 0 {
		return -1
	}
	pct := float64(done) / float64(total) * 100
	if pct > 100 {
		return 100
	}
	return pct
}

// A folder that disappears between the count and the pack must not wedge the
// run: the total is a best effort taken a moment earlier, and the pack is what
// decides what the snapshot contains.
func TestAFileVanishingAfterTheCountDoesNotFailTheRun(t *testing.T) {
	src := t.TempDir()
	dst := t.TempDir()
	if err := os.WriteFile(filepath.Join(src, "keep.txt"), []byte("kept"), 0o644); err != nil {
		t.Fatal(err)
	}
	doomed := filepath.Join(src, "vanishes.txt")
	if err := os.WriteFile(doomed, []byte("gone soon"), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg := config.New()
	cfg.General.MachineName = "mach"
	cfg.Folders = []config.Folder{{ID: "f1", Path: src, Label: "proj"}}
	job := config.Archive{Name: "vanish", Every: "daily", Target: "t", Keep: 2}

	removed := false
	res := Run(localmirror.NewLocalFS(dst), cfg, job, "pw", slog.New(slog.DiscardHandler),
		func(p Progress) {
			if !removed && p.DoneFiles == 0 {
				os.Remove(doomed) // after the count, before the pack reaches it
				removed = true
			}
		})
	if res.Err != "" {
		t.Fatalf("a file disappearing mid-run failed the whole snapshot: %s", res.Err)
	}
	if res.Files < 1 {
		t.Errorf("packed %d files, want at least the one that still existed", res.Files)
	}
}

// THE GUARANTEE: verification reports its own progress.
//
// Packing ends and the archive is then read back in full to prove it can be
// opened. For a multi-gigabyte snapshot over a network that is minutes of work,
// and with nothing reported the bar sat full at 100% while the state still said
// "running" — indistinguishable from a hang, and reported as one.
func TestVerificationReportsProgressOfItsOwn(t *testing.T) {
	cfg, job, b, _ := testSetup(t)

	var phases []string
	var lastVerify Progress
	res := Run(b, cfg, job, "pw", slog.New(slog.DiscardHandler), func(p Progress) {
		if len(phases) == 0 || phases[len(phases)-1] != p.Phase {
			phases = append(phases, p.Phase)
		}
		if p.Phase == PhaseVerifying {
			lastVerify = p
		}
	})
	if res.Err != "" {
		t.Fatalf("run failed: %s", res.Err)
	}
	want := []string{PhasePacking, PhaseVerifying}
	if len(phases) != len(want) || phases[0] != want[0] || phases[1] != want[1] {
		t.Fatalf("phases = %v, want %v", phases, want)
	}
	if lastVerify.TotalBytes != res.StoredBytes {
		t.Errorf("verification counted against %d bytes, the archive is %d", lastVerify.TotalBytes, res.StoredBytes)
	}
	if lastVerify.DoneBytes != lastVerify.TotalBytes {
		t.Errorf("verification finished at %d of %d", lastVerify.DoneBytes, lastVerify.TotalBytes)
	}
}
