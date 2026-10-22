// SPDX-License-Identifier: MIT

package daemon

import (
	"testing"
	"time"

	"github.com/phil9922/backup-maker/internal/archive"
	"github.com/phil9922/backup-maker/internal/config"
)

// A failing snapshot backs off rather than retrying every scheduler tick, and
// never waits so long that a destination coming home is left unprotected.
func TestAFailingSnapshotBacksOffButNotForLong(t *testing.T) {
	for attempts, want := range map[int]time.Duration{
		1: time.Minute, 2: 2 * time.Minute, 3: 4 * time.Minute, 5: 16 * time.Minute,
		6: 30 * time.Minute, 50: 30 * time.Minute, 44440: 30 * time.Minute,
	} {
		if got := archiveRetryDelay(attempts); got != want {
			t.Errorf("after %d failures the wait is %v, want %v", attempts, got, want)
		}
	}
}

// The record of how long a schedule has been failing survives a restart and
// counts every attempt, and one success clears it.
func TestHowLongASnapshotHasBeenFailingSurvivesARestart(t *testing.T) {
	isolateState(t)
	first := &daemon{state: &config.State{}}
	began := time.Now().Add(-24 * 24 * time.Hour)
	first.recordArchiveResult(archive.Result{ArchiveName: "nightly", When: began, Err: "write protected"}, false)
	first.recordArchiveResult(archive.Result{ArchiveName: "nightly", When: began.Add(time.Hour), Err: "write protected"}, false)

	if next := first.nextArchiveAttempt("nightly"); !next.After(time.Now()) {
		t.Fatalf("a job that just failed may run again at %v, which is not later than now", next)
	}

	reloaded, err := config.LoadState()
	if err != nil {
		t.Fatal(err)
	}
	second := &daemon{state: reloaded}
	if next := second.nextArchiveAttempt("nightly"); !next.IsZero() {
		t.Errorf("after a restart the job waits until %v; it should be tried straight away", next)
	}
	second.recordArchiveResult(archive.Result{ArchiveName: "nightly", When: time.Now(), Err: "write protected"}, false)
	res := second.archiveResults["nightly"]
	if !res.FailingSince.Equal(began) {
		t.Errorf("after a restart the job reads as failing since %v, want %v", res.FailingSince, began)
	}
	if res.Attempts != 3 {
		t.Errorf("attempts = %d, want 3", res.Attempts)
	}

	second.recordArchiveResult(archive.Result{ArchiveName: "nightly", When: time.Now()}, true)
	if _, still := second.state.ArchiveFailing["nightly"]; still {
		t.Error("a successful snapshot left the job recorded as failing")
	}
	if next := second.nextArchiveAttempt("nightly"); !next.IsZero() {
		t.Errorf("a job that just succeeded is held back until %v", next)
	}
}
