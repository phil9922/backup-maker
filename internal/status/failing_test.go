// SPDX-License-Identifier: MIT

package status

import (
	"strings"
	"testing"
	"time"
)

// A failed schedule says how long it has been failing and how often, so a
// last success weeks ago cannot be read as one failure last night.
func TestAFailingSnapshotSaysHowLongItHasBeenFailing(t *testing.T) {
	since := time.Date(2026, 9, 28, 14, 34, 0, 0, time.Local)
	got := FailingSummary(ArchiveRow{
		State: "failed", FailingSince: since, Attempts: 44440,
		NextAttempt: time.Now().Add(29*time.Minute + 50*time.Second),
	})
	for _, want := range []string{"failing since 28 Sep 14:34", "44440 attempts", "trying again in 30m"} {
		if !strings.Contains(got, want) {
			t.Errorf("summary %q does not say %q", got, want)
		}
	}
	if strings.Contains(got, "0s") {
		t.Errorf("summary %q spells the wait the way Go does, not the way people do", got)
	}
	if FailingSummary(ArchiveRow{State: "failed"}) != "" {
		t.Error("a failure with no record of when it began claimed a duration")
	}
}
