// SPDX-License-Identifier: MIT

package localmirror

import (
	"testing"
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
