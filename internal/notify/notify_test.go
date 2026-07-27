// SPDX-License-Identifier: MIT

package notify

import (
	"context"
	"testing"
	"time"
)

// The headless case, which is not an error: no helper binary to run. It must
// come back promptly and say so, rather than costing the caller a timeout.
//
// Nothing here calls Desktop().Notify — running the test suite must not pop
// notifications on the machine running it.
func TestRunReportsAMissingHelperImmediately(t *testing.T) {
	start := time.Now()
	err := run(context.Background(), "backup-maker-no-such-notifier-helper", "hello")
	if err == nil {
		t.Fatal("a missing helper reported success")
	}
	if took := time.Since(start); took > time.Second {
		t.Errorf("a missing helper took %v to report", took)
	}
}

// A cancelled context is honoured rather than ignored: the status loop's
// guarantee rests on this deadline being real.
func TestRunHonoursACancelledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := run(ctx, "backup-maker-no-such-notifier-helper"); err == nil {
		t.Fatal("a cancelled context reported success")
	}
}

func TestOneLineFlattensText(t *testing.T) {
	got := oneLine("Last seen 8 days ago.\nNothing has been\tbacked up since.")
	want := "Last seen 8 days ago. Nothing has been backed up since."
	if got != want {
		t.Errorf("oneLine = %q, want %q", got, want)
	}
}
