// SPDX-License-Identifier: MIT

package config

import (
	"testing"
	"time"
)

func at(min int) time.Time {
	return time.Date(2026, 7, 30, 3, min, 0, 0, time.UTC)
}

func titles(recs []AlertRecord) []string {
	out := make([]string, 0, len(recs))
	for _, r := range recs {
		out = append(out, r.Title)
	}
	return out
}

func same(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// Delivery happens on its own goroutine, so two alerts raised a moment apart
// can finish out of order. A history that is not in time order is one somebody
// has to read twice before they can trust it.
func TestAlertsAreKeptInTimeOrderHoweverTheyArrive(t *testing.T) {
	var s State
	s.RecordAlert(AlertRecord{At: at(3), Title: "third"})
	s.RecordAlert(AlertRecord{At: at(1), Title: "first"})
	s.RecordAlert(AlertRecord{At: at(4), Title: "fourth"})
	s.RecordAlert(AlertRecord{At: at(2), Title: "second"})

	want := []string{"first", "second", "third", "fourth"}
	if got := titles(s.RecentAlerts); !same(got, want) {
		t.Errorf("history is out of order:\n got %v\nwant %v", got, want)
	}
}

// The cap has to drop the OLDEST. Dropping the newest would mean a machine
// alerting steadily kept only the first fifty things that ever happened to it,
// which is the opposite of useful.
func TestTheHistoryKeepsTheNewestAndDropsTheOldest(t *testing.T) {
	var s State
	for i := 0; i < MaxAlertHistory+10; i++ {
		s.RecordAlert(AlertRecord{At: at(i), Title: string(rune('a' + i%26))})
	}
	if len(s.RecentAlerts) != MaxAlertHistory {
		t.Fatalf("kept %d alerts, want the cap of %d", len(s.RecentAlerts), MaxAlertHistory)
	}
	oldest, newest := s.RecentAlerts[0], s.RecentAlerts[len(s.RecentAlerts)-1]
	if !oldest.At.Equal(at(10)) {
		t.Errorf("oldest kept is %v, want the 11th raised — the cap dropped the wrong end", oldest.At)
	}
	if !newest.At.Equal(at(MaxAlertHistory + 9)) {
		t.Errorf("newest kept is %v, want the last raised", newest.At)
	}
}

// An out-of-order arrival once the history is full must not resurrect an
// already-dropped alert or lose the newest one.
func TestALateArrivalOnAFullHistoryStillLeavesTheNewestInPlace(t *testing.T) {
	var s State
	for i := 0; i < MaxAlertHistory; i++ {
		s.RecordAlert(AlertRecord{At: at(i + 10), Title: "regular"})
	}
	s.RecordAlert(AlertRecord{At: at(0), Title: "late and old"})

	if len(s.RecentAlerts) != MaxAlertHistory {
		t.Fatalf("kept %d, want %d", len(s.RecentAlerts), MaxAlertHistory)
	}
	if s.RecentAlerts[0].Title == "late and old" {
		t.Error("an alert older than everything kept displaced a newer one")
	}
	if last := s.RecentAlerts[len(s.RecentAlerts)-1]; !last.At.Equal(at(MaxAlertHistory + 9)) {
		t.Errorf("the newest alert was lost: %v", last.At)
	}
}
