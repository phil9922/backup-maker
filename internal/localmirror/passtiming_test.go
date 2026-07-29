// SPDX-License-Identifier: MIT

package localmirror

import (
	"log/slog"
	"strings"
	"testing"
	"time"
)

// timedEngine returns an engine whose log is captured, and the buffer holding
// it. The handler is left at its default level so Debug is discarded, which is
// what the "stays out of the log" case below is actually asserting.
func timedEngine(t *testing.T) (*Engine, *strings.Builder) {
	t.Helper()
	var logs strings.Builder
	e := New(Options{
		FolderID: "f1", TargetName: "dest", SourcePath: t.TempDir(),
		Backend: NewLocalFS(t.TempDir()), MachineName: "mach", Label: "proj",
		MaxAgeDays: 30, Log: slog.New(slog.NewTextHandler(&logs, nil)),
	})
	return e, &logs
}

// THE GUARANTEE: a pass that took a long time and copied nothing still says so.
//
// This is the case that had no trace at all. The log line was conditional on
// something having been copied, so a destination that spent eleven and a half
// minutes concluding it had nothing to do wrote nothing, and the minutes could
// only be found as a gap between two other lines. That gap is precisely what
// gets reported as "the daemon has stopped working", and the answer has to be
// recoverable after the fact — nobody is watching the dashboard when it happens.
func TestASlowPassThatCopiedNothingIsStillLogged(t *testing.T) {
	e, logs := timedEngine(t)
	e.beginPass()
	e.passStart = time.Now().Add(-12 * time.Minute)

	e.logPass(0, 0)

	if !strings.Contains(logs.String(), "synced") {
		t.Errorf("a 12-minute pass that copied nothing logged nothing:\n%s", logs.String())
	}
	if !strings.Contains(logs.String(), "took=12m") {
		t.Errorf("the pass log does not say how long it took:\n%s", logs.String())
	}
}

// The other half: a quiet machine stays quiet. This runs on every pass of every
// destination, so a line each time saying nothing happened quickly is how a log
// becomes something nobody reads — and then the slow line above gets missed too.
func TestAQuickUneventfulPassStaysOutOfTheLog(t *testing.T) {
	e, logs := timedEngine(t)
	e.beginPass()

	e.logPass(0, 0)

	if strings.Contains(logs.String(), "synced") {
		t.Errorf("a fast pass with nothing to do logged at info:\n%s", logs.String())
	}
}

// A pass that copied something is worth a line however fast it was: that line is
// the record that the backup actually happened.
func TestAPassThatCopiedSomethingIsAlwaysLogged(t *testing.T) {
	e, logs := timedEngine(t)
	e.beginPass()

	e.logPass(3, 0)

	if !strings.Contains(logs.String(), "copied=3") {
		t.Errorf("a pass that copied files logged nothing:\n%s", logs.String())
	}
}

// THE GUARANTEE: the pass log says which stretch of the pass spent the time.
//
// "It took eleven minutes" only raises the next question. The stages are worth
// separating because they fail differently: a slow source walk is a local disk
// problem, and a slow listing or directory sweep is a round-trip-per-item
// problem with a network share on the other end.
func TestThePassLogSaysWhereTheTimeWent(t *testing.T) {
	e, logs := timedEngine(t)
	e.beginPass()
	e.beginStage("source")
	e.beginStage("listing")
	e.beginStage("dirs")
	e.passStart = time.Now().Add(-time.Hour) // slow enough to be logged

	e.logPass(0, 0)

	for _, stage := range []string{"source=", "listing=", "dirs="} {
		if !strings.Contains(logs.String(), stage) {
			t.Errorf("pass log has no %q timing:\n%s", stage, logs.String())
		}
	}
}

// The directory sweep is timed separately while still being "tidying" on screen.
// The two carvings are deliberately different — one is worded for a person
// reading a dashboard, the other is where the cost is — and a change that
// collapses them takes away the measurement that found the slow sweep.
func TestTheDirectorySweepIsTimedApartFromTheRestOfTheTidyUp(t *testing.T) {
	e, logs := timedEngine(t)
	e.beginPass()
	e.beginScanPhase("tidying", 10)
	e.beginStage("dirs")

	if got := e.Status().Phase; got != "tidying" {
		t.Errorf("Status().Phase = %q during the directory sweep, want %q: the "+
			"dashboard wording must not follow the timing split", got, "tidying")
	}
	e.passStart = time.Now().Add(-time.Hour)
	e.logPass(0, 0)
	out := logs.String()
	if !strings.Contains(out, "tidying=") || !strings.Contains(out, "dirs=") {
		t.Errorf("tidying and dirs are not timed separately:\n%s", out)
	}
}
