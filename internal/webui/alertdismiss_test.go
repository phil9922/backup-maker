// SPDX-License-Identifier: MIT

package webui

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// dismissServer wires just the two actions this route needs and records what
// they were asked to do.
func dismissServer(t *testing.T) (*Server, *[]string) {
	t.Helper()
	var calls []string
	s := &Server{actions: Actions{
		DismissAlert: func(at time.Time) error {
			calls = append(calls, "one:"+at.UTC().Format(time.RFC3339Nano))
			return nil
		},
		DismissAlertsBefore: func(cutoff time.Time) error {
			calls = append(calls, "before:"+cutoff.UTC().Format(time.RFC3339Nano))
			return nil
		},
	}}
	return s, &calls
}

func dismissPost(body string) *http.Request {
	return httptest.NewRequest(http.MethodPost, "/api/alerts/dismiss", strings.NewReader(body))
}

// One entry, keyed on the instant it was raised — which is what the dashboard
// has, since an AlertRecord carries no id.
func TestDismissingOneAlertReachesTheDaemon(t *testing.T) {
	s, calls := dismissServer(t)
	rec := httptest.NewRecorder()
	s.handleDismissAlert(rec, dismissPost(`{"at":"2026-08-03T10:00:00.123456789Z"}`))

	if rec.Code != http.StatusOK {
		t.Fatalf("dismissing returned %d: %s", rec.Code, rec.Body.String())
	}
	if len(*calls) != 1 || (*calls)[0] != "one:2026-08-03T10:00:00.123456789Z" {
		t.Errorf("the daemon was asked for %v", *calls)
	}
}

// "Dismiss all" is the same act at a wider scope, and it is BOUNDED. The
// dashboard sends the newest alert it actually drew rather than "everything",
// so an alert raised between the page rendering and the click survives to be
// read — the one outcome worse than a section that will not clear.
func TestDismissAllIsBoundedByATime(t *testing.T) {
	s, calls := dismissServer(t)
	rec := httptest.NewRecorder()
	s.handleDismissAlert(rec, dismissPost(`{"before":"2026-08-03T10:00:00Z"}`))

	if rec.Code != http.StatusOK {
		t.Fatalf("returned %d: %s", rec.Code, rec.Body.String())
	}
	if len(*calls) != 1 || (*calls)[0] != "before:2026-08-03T10:00:00Z" {
		t.Errorf("the daemon was asked for %v", *calls)
	}
}

// A request naming nothing must not be read as "clear everything". The
// difference between an empty body and an explicit cutoff is the difference
// between a bug and an instruction.
func TestDismissingNothingInParticularIsRefused(t *testing.T) {
	for _, body := range []string{`{}`, `{"at":""}`, `{"at":"","before":""}`} {
		s, calls := dismissServer(t)
		rec := httptest.NewRecorder()
		s.handleDismissAlert(rec, dismissPost(body))
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%s returned %d, want 400", body, rec.Code)
		}
		if len(*calls) != 0 {
			t.Errorf("%s still reached the daemon as %v", body, *calls)
		}
		// The MESSAGE is the part worth pinning. An empty request is refused
		// either way — an empty string is not a parseable time — so the only
		// thing the explicit check buys is telling somebody what they left out
		// instead of complaining about a time they never sent.
		if !strings.Contains(rec.Body.String(), "say which alert to dismiss") {
			t.Errorf("%s was refused with an unhelpful message: %s", body, rec.Body.String())
		}
	}
}

func TestAnUnreadableTimeIsRefused(t *testing.T) {
	s, calls := dismissServer(t)
	rec := httptest.NewRecorder()
	s.handleDismissAlert(rec, dismissPost(`{"at":"last tuesday"}`))
	if rec.Code != http.StatusBadRequest {
		t.Errorf("returned %d, want 400", rec.Code)
	}
	if len(*calls) != 0 {
		t.Errorf("it still reached the daemon as %v", *calls)
	}
}

// The route answers 503 rather than 200-and-nothing when it is not wired.
func TestDismissingIsUnavailableWhenNotWired(t *testing.T) {
	s := &Server{}
	rec := httptest.NewRecorder()
	s.handleDismissAlert(rec, dismissPost(`{"at":"2026-08-03T10:00:00Z"}`))
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("returned %d, want 503", rec.Code)
	}
}

// An alert body quotes the failure and names destinations, so the whole history
// is stripped from the unauthenticated network view. Dismissal state rides on
// those records and must not be the thing that reintroduces them.
func TestTheAlertHistoryStillNeverReachesTheNetworkView(t *testing.T) {
	redacted, ok := RedactForNetwork(map[string]any{
		"machine_name": "my-laptop",
		"recent_alerts": []any{map[string]any{
			"at": "2026-08-03T10:00:00Z", "title": "backups to nas-attic are stale",
			"dismissed_at": "2026-08-03T11:00:00Z",
		}},
	}).(map[string]any)
	if !ok {
		t.Fatal("redaction did not return an object")
	}
	if _, still := redacted["recent_alerts"]; still {
		t.Error("the alert history reached the network view")
	}
}
