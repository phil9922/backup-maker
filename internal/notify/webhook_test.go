// SPDX-License-Identifier: MIT

package notify

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func capture(t *testing.T, status int) (*Webhook, *[]WebhookPayload, *httptest.Server) {
	t.Helper()
	var got []WebhookPayload
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var p WebhookPayload
		_ = json.Unmarshal(body, &p)
		got = append(got, p)
		w.WriteHeader(status)
	}))
	t.Cleanup(srv.Close)
	return &Webhook{URL: srv.URL, Machine: "my-laptop", Client: srv.Client()}, &got, srv
}

// The ordinary case: a receiver gets enough to be useful.
func TestAWebhookCarriesTheAlert(t *testing.T) {
	w, got, _ := capture(t, http.StatusOK)
	if err := w.Notify(context.Background(), Critical, "card is stale", "Backups have not reached it for 3 days."); err != nil {
		t.Fatalf("delivery failed: %v", err)
	}
	if len(*got) != 1 {
		t.Fatalf("receiver got %d payloads, want 1", len(*got))
	}
	p := (*got)[0]
	if p.Level != "critical" {
		t.Errorf("level = %q, want critical — a receiver decides whether to wake somebody on this", p.Level)
	}
	if p.Title != "card is stale" || p.Body == "" {
		t.Errorf("the alert did not survive the trip: %+v", p)
	}
	if p.Machine != "my-laptop" {
		t.Errorf("machine = %q, want my-laptop", p.Machine)
	}
	if p.Source != "backup-maker" {
		t.Errorf("source = %q — a receiver fed by several things cannot tell them apart", p.Source)
	}
	if p.Time.IsZero() {
		t.Error("no timestamp")
	}
}

// THE WHOLE POINT OF MINIMAL MODE. The last hop to a phone crosses somebody
// else's server. In this mode that server must learn that something needs
// attention and nothing else — not the machine's name, not a destination, not
// a folder.
func TestMinimalModeTellsTheRelayNothingAboutTheHousehold(t *testing.T) {
	w, got, _ := capture(t, http.StatusOK)
	w.Minimal = true

	err := w.Notify(context.Background(), Critical,
		"nas-attic is stale", "Backups to /mnt/photos have not arrived for 3 days.")
	if err != nil {
		t.Fatalf("delivery failed: %v", err)
	}
	p := (*got)[0]
	raw, _ := json.Marshal(p)
	for _, secret := range []string{"nas-attic", "my-laptop", "/mnt/photos", "photos"} {
		if strings.Contains(string(raw), secret) {
			t.Errorf("minimal mode leaked %q to the relay: %s", secret, raw)
		}
	}
	// It still has to be actionable enough to be worth sending.
	if p.Level != "critical" {
		t.Errorf("severity was lost: %+v", p)
	}
	if p.Title == "" {
		t.Error("nothing to display: the phone would buzz with an empty notification")
	}
}

// A receiver that rejects the post is a failure the caller must hear about —
// that is what makes the Test button in the dashboard worth pressing.
func TestARefusingReceiverIsReportedAsAFailure(t *testing.T) {
	w, _, _ := capture(t, http.StatusInternalServerError)
	err := w.Notify(context.Background(), Normal, "hello", "")
	if err == nil {
		t.Fatal("a 500 from the receiver was reported as success")
	}
	if !strings.Contains(err.Error(), "500") {
		t.Errorf("the error does not say what happened: %v", err)
	}
}

func TestAnUnconfiguredWebhookSaysSoRatherThanPosting(t *testing.T) {
	var w Webhook
	if err := w.Notify(context.Background(), Normal, "x", ""); err == nil {
		t.Fatal("an empty URL was treated as deliverable")
	}
}

// failing is a sink that never works.
type failing struct{ called *int }

func (f failing) Notify(context.Context, Urgency, string, string) error {
	*f.called++
	return errors.New("nope")
}

type working struct{ called *int }

func (w working) Notify(context.Context, Urgency, string, string) error {
	*w.called++
	return nil
}

// ONE BROKEN SINK MUST NOT SILENCE ANOTHER. A webhook pointing at a host that
// has gone away would otherwise take the desktop notification down with it —
// and the user would lose the alerting they already had by configuring the one
// that broke.
func TestABrokenSinkDoesNotStopTheOthers(t *testing.T) {
	var bad, good int
	m := Multi{{Method: "webhook", Notifier: failing{&bad}}, {Method: "desktop", Notifier: working{&good}}}

	err := m.Notify(context.Background(), Critical, "card is stale", "")
	if good != 1 {
		t.Error("the working sink was skipped because an earlier one failed")
	}
	if bad != 1 {
		t.Error("the failing sink was not tried")
	}
	if err == nil {
		t.Error("the failure was swallowed; nothing would ever report a dead webhook")
	}
}

// A failure has to be ATTRIBUTABLE. With two methods switched on, "an alert
// could not be delivered" is not something a user can act on — they need to
// know which one stopped working.
func TestAFanoutSaysWhichMethodFailed(t *testing.T) {
	var bad, good int
	m := Multi{{Method: "webhook", Notifier: failing{&bad}}, {Method: "desktop", Notifier: working{&good}}}

	results := m.Deliver(context.Background(), Critical, "card is stale", "")
	if len(results) != 2 {
		t.Fatalf("got %d results, want one per method", len(results))
	}
	by := map[string]Result{}
	for _, r := range results {
		by[r.Method] = r
	}
	if by["webhook"].OK() {
		t.Error("the broken webhook was reported as delivered")
	}
	if !by["desktop"].OK() {
		t.Error("the working desktop sink was reported as failed")
	}
	if by["webhook"].At.IsZero() {
		t.Error("no timestamp: the dashboard cannot say WHEN it last failed")
	}
}
