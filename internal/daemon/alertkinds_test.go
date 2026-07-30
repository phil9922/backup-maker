// SPDX-License-Identifier: MIT

package daemon

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/phil9922/backup-maker/internal/config"
	"github.com/phil9922/backup-maker/internal/notify"
	"github.com/phil9922/backup-maker/internal/status"
)

func off() *bool { v := false; return &v }

// countingNotifier records deliveries that got past the gate.
type countingNotifier struct {
	mu sync.Mutex
	n  int
}

func (c *countingNotifier) Notify(context.Context, notify.Urgency, string, string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.n++
	return nil
}

func (c *countingNotifier) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.n
}

func kindsAlerter(k config.AlertKinds) *alerter {
	a := newAlerter(nil, slog.New(slog.DiscardHandler), true)
	a.setKinds(k)
	return a
}

// brokenModel is a destination that has stopped receiving backups, plus a
// failed snapshot job and a machine waiting to pair — one of everything, so a
// single fixture can show which categories speak and which stay quiet.
func brokenModel() status.Model {
	return status.Model{
		Targets:  []status.TargetInfo{{Name: "card", State: "stale"}},
		Archives: []status.ArchiveRow{{Name: "nightly", Target: "card", State: "failed"}},
	}
}

// The setting has to actually silence the category — not merely be stored.
func TestSwitchingOffACategorySilencesIt(t *testing.T) {
	now := time.Now()

	all := kindsAlerter(config.AlertKinds{})
	got := all.pending(brokenModel(), now)
	if len(got) != 2 {
		t.Fatalf("with every category on, expected the destination and the snapshot: %s", titles(got))
	}

	quiet := kindsAlerter(config.AlertKinds{SnapshotFailed: off()})
	got = quiet.pending(brokenModel(), now)
	if len(got) != 1 || !strings.Contains(got[0].title, "card") {
		t.Errorf("switching off snapshot alerts changed the wrong thing: %s", titles(got))
	}

	silent := kindsAlerter(config.AlertKinds{SnapshotFailed: off(), BackupsStopped: off()})
	if got = silent.pending(brokenModel(), now); len(got) != 0 {
		t.Errorf("both categories off still produced: %s", titles(got))
	}
}

// EVERY STICKY ALERT OWES AN ALL-CLEAR — and the corollary this setting creates:
// a category that is switched off must not deliver the all-clear for a warning
// it never gave. A notification saying "backups have resumed" to somebody who
// was never told they stopped is nonsense, and worse, implies the machine is
// watching something it is not.
func TestASilencedCategoryDoesNotDeliverAnOrphanAllClear(t *testing.T) {
	now := time.Now()
	a := kindsAlerter(config.AlertKinds{})

	// Told once, while the category is on.
	if got := a.pending(brokenModel(), now); len(got) == 0 {
		t.Fatal("the fault was never announced in the first place")
	}

	// The user switches destination alerts off, then the destination recovers.
	a.setKinds(config.AlertKinds{BackupsStopped: off()})
	healthy := status.Model{
		Targets:  []status.TargetInfo{{Name: "card", State: "in sync"}},
		Archives: []status.ArchiveRow{{Name: "nightly", Target: "card", State: "failed"}},
	}
	for _, al := range a.pending(healthy, now.Add(time.Minute)) {
		if strings.Contains(al.title, "card") {
			t.Errorf("a silenced category delivered an all-clear: %q", al.title)
		}
	}
}

// Switching a category back ON must report a problem that is already there.
// The alternative — comparing against history nobody was shown — leaves the
// user with alerts enabled, a broken destination, and silence.
func TestSwitchingACategoryBackOnReportsAFaultAlreadyPresent(t *testing.T) {
	now := time.Now()
	a := kindsAlerter(config.AlertKinds{BackupsStopped: off()})

	if got := a.pending(brokenModel(), now); len(got) != 1 {
		t.Fatalf("expected only the snapshot alert while destinations are silenced: %s", titles(got))
	}

	a.setKinds(config.AlertKinds{})
	got := a.pending(brokenModel(), now.Add(time.Minute))
	var announced bool
	for _, al := range got {
		if strings.Contains(al.title, "card") {
			announced = true
		}
	}
	if !announced {
		t.Errorf("switching the category back on stayed silent about a destination that is broken right now: %s", titles(got))
	}
}

// The master switch still wins: with desktop alerts off entirely, no category
// speaks however it is configured.
func TestTheMasterSwitchOverridesEveryCategory(t *testing.T) {
	a := newAlerter(nil, slog.New(slog.DiscardHandler), false)
	a.setKinds(config.AlertKinds{})
	if got := a.pending(brokenModel(), time.Now()); len(got) != 0 {
		t.Errorf("alerts were off and it still produced: %s", titles(got))
	}
	// Including the two that do not go through pending() at all.
	a.foreignStorage("card", "/media/alex/CARD")
	a.storageRecognized("card", "/media/alex/CARD")
}

// The unrecognised-storage pair is announced from mayWrite rather than from the
// model, so it needs its own gate — and its own proof that the gate is there.
func TestSwitchingOffUnrecognisedStorageSilencesBothHalves(t *testing.T) {
	sent := &countingNotifier{}
	a := kindsAlerter(config.AlertKinds{UnrecognisedStorage: off()})
	a.notifier = sent

	a.foreignStorage("card", "/media/alex/CARD")
	a.storageRecognized("card", "/media/alex/CARD")
	// Delivery is asynchronous; the gate is not. If the gate were missing these
	// would have been queued, which is what the counter would eventually show.
	time.Sleep(50 * time.Millisecond)
	if n := sent.count(); n != 0 {
		t.Errorf("a silenced category still sent %d notification(s)", n)
	}
}

// ALERTING THAT HAS STOPPED WORKING IS THE ONE FAULT THIS PROGRAM CANNOT
// ANNOUNCE. If the route an alert would travel is broken, the alert never
// arrives to say so — so the outcome of every delivery is recorded per method
// and shown on the page instead.
func TestAFailedDeliveryIsRecordedAgainstItsOwnMethod(t *testing.T) {
	log := newDeliveryLog()
	var bad, good int
	a := kindsAlerter(config.AlertKinds{})
	a.delivered = log.record
	a.setNotifier(notify.Multi{
		{Method: "webhook", Notifier: failingSink{&bad}},
		{Method: "desktop", Notifier: workingSink{&good}},
	})

	a.send(alert{urgency: notify.Critical, title: "card is stale"})
	// Delivery is asynchronous by design; the record lands with it.
	deadline := time.Now().Add(2 * time.Second)
	var snap []status.DeliveryInfo
	for time.Now().Before(deadline) {
		if snap = log.snapshot(); len(snap) == 2 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if len(snap) != 2 {
		t.Fatalf("recorded %d methods, want one per sink", len(snap))
	}
	by := map[string]status.DeliveryInfo{}
	for _, s := range snap {
		by[s.Method] = s
	}
	if by["webhook"].OK {
		t.Error("a webhook that refused the alert was recorded as delivered")
	}
	if by["webhook"].Error == "" {
		t.Error("no reason recorded: the panel would have nothing to show the user")
	}
	if !by["desktop"].OK {
		t.Error("the working method was recorded as failed")
	}
	if by["desktop"].At.IsZero() {
		t.Error("no timestamp: the panel cannot say when it last worked")
	}
}

type failingSink struct{ n *int }

func (f failingSink) Notify(context.Context, notify.Urgency, string, string) error {
	*f.n++
	return errors.New("connection refused")
}

type workingSink struct{ n *int }

func (w workingSink) Notify(context.Context, notify.Urgency, string, string) error {
	*w.n++
	return nil
}
