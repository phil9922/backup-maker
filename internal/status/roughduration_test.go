// SPDX-License-Identifier: MIT

package status

import (
	"strings"
	"testing"
	"time"
)

// "trying again in ." was a real alert: under 30 seconds rounds to 0, which
// prints as "0s", and trimming "0s" left nothing.
func TestARetryUnderAMinuteAwaySaysSo(t *testing.T) {
	for d, want := range map[time.Duration]string{
		10 * time.Second:             "under a minute",
		90 * time.Second:             "2m",
		30 * time.Minute:             "30m",
		time.Hour:                    "1h",
		time.Hour + 5*time.Minute:    "1h5m",
		2*time.Hour + 29*time.Second: "2h",
	} {
		if got := roughDuration(d); got != want {
			t.Errorf("roughDuration(%v) = %q, want %q", d, got, want)
		}
	}
	row := ArchiveRow{FailingSince: time.Now().Add(-time.Hour), Attempts: 3, NextAttempt: time.Now().Add(10 * time.Second)}
	if got := FailingSummary(row); !strings.HasSuffix(got, "trying again in under a minute") {
		t.Fatalf("FailingSummary = %q", got)
	}
}
