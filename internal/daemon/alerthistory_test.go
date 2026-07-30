// SPDX-License-Identifier: MIT

package daemon

import (
	"context"
	"errors"
	"log/slog"
	"testing"
	"time"

	"github.com/phil9922/backup-maker/internal/config"
	"github.com/phil9922/backup-maker/internal/notify"
)

// recordingAlerter is an alerter whose history goes to a slice instead of
// state.json, with a way to wait for the delivery goroutine.
// want(n) collects exactly n records, failing if fewer arrive — and, for n
// alerts that were meant to be suppressed, want(0) confirms none does.
func recordingAlerter(t *testing.T, sink notify.Notifier) (*alerter, func(int) []config.AlertRecord) {
	t.Helper()
	a := newAlerter(sink, slog.New(slog.DiscardHandler), true)
	got := make(chan config.AlertRecord, 16)
	a.recorded = func(r config.AlertRecord) { got <- r }
	want := func(n int) []config.AlertRecord {
		t.Helper()
		out := make([]config.AlertRecord, 0, n)
		for len(out) < n {
			select {
			case r := <-got:
				out = append(out, r)
			case <-time.After(3 * time.Second):
				t.Fatalf("only %d of %d alerts reached the history: %+v", len(out), n, out)
			}
		}
		// Nothing extra may follow: a duplicate record is as wrong as a
		// missing one, and would double-count a fault in the history.
		select {
		case extra := <-got:
			t.Fatalf("an unexpected extra record arrived: %+v", extra)
		case <-time.After(150 * time.Millisecond):
		}
		return out
	}
	return a, want
}

type stubSink struct{ err error }

func (s stubSink) Notify(context.Context, notify.Urgency, string, string) error { return s.err }

// THE MOST IMPORTANT LINE THIS HISTORY CAN HOLD. An alert raised on a machine
// with no delivery method configured — a headless Pi, or a laptop with desktop
// alerts off — goes nowhere at all, and that is invisible in every other part
// of the program. If it were not recorded, the history would be silent about
// exactly the machines whose alerting is silent.
func TestAnAlertNobodyCouldDeliverIsStillRecorded(t *testing.T) {
	a, want := recordingAlerter(t, nil)
	a.send(alert{urgency: notify.Critical, title: "card is full", body: "no room left"})

	r := want(1)[0]
	if r.Title != "card is full" || r.Body != "no room left" {
		t.Errorf("record does not carry the alert: %+v", r)
	}
	if !r.Urgent {
		t.Error("a critical alert was not recorded as urgent")
	}
	if len(r.Delivered) != 0 || len(r.Failed) != 0 {
		t.Errorf("nothing was tried, so nothing should be claimed: %+v", r)
	}
	if r.At.IsZero() {
		t.Error("the record has no timestamp")
	}
}

// Per-method outcomes, which is what makes "your webhook has stopped working"
// readable after the fact rather than only in the moment.
func TestTheHistoryRecordsWhichMethodsTookAnAlertAndWhichFailed(t *testing.T) {
	a, want := recordingAlerter(t, notify.Multi{
		{Method: "desktop", Notifier: stubSink{}},
		{Method: "webhook", Notifier: stubSink{err: errors.New("connection refused")}},
	})
	a.send(alert{urgency: notify.Normal, title: "backup-pi is backing up again"})

	r := want(1)[0]
	if len(r.Delivered) != 1 || r.Delivered[0] != "desktop" {
		t.Errorf("Delivered = %v, want [desktop]", r.Delivered)
	}
	if len(r.Failed) != 1 || r.Failed[0] != "webhook" {
		t.Errorf("Failed = %v, want [webhook]", r.Failed)
	}
	if r.Urgent {
		t.Error("an all-clear was recorded as urgent")
	}
}

// Recording hangs off send(), which every emission point in alerts.go goes
// through. This is the test that says so: if somebody adds a tenth kind of
// alert that bypasses send, or an early return in send skips the record, the
// count here stops matching.
func TestEveryKindOfAlertReachesTheHistory(t *testing.T) {
	a, want := recordingAlerter(t, nil)
	a.setKinds(config.AlertKinds{})

	a.foreignStorage("card", "/media/card")
	a.storageRecognized("card", "/media/card")
	a.nameClash("card", "/media/card", "my-laptop")
	a.nameClashResolved("card", "/media/card")
	a.updateAvailable("v9.9.9")
	a.lanDeviceWaiting("123456", "A phone")

	recs := want(6)
	seen := map[string]bool{}
	for _, r := range recs {
		seen[r.Title] = true
		if r.At.IsZero() {
			t.Errorf("a record has no timestamp: %+v", r)
		}
	}
	if len(seen) != 6 {
		t.Errorf("expected six distinct alerts, got %d: %v", len(seen), seen)
	}
}

// Nothing is recorded for an alert that was never raised: the category
// switches still decide, and a history full of alerts the user asked not to
// receive would be a record of a program ignoring its settings.
func TestAnAlertTheUserSwitchedOffIsNotRecorded(t *testing.T) {
	a, want := recordingAlerter(t, nil)
	off := false
	a.setKinds(config.AlertKinds{UnrecognisedStorage: &off})

	a.foreignStorage("card", "/media/card")

	// want(0) fails if anything at all arrives.
	want(0)
}
