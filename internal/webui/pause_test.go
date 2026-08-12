// SPDX-License-Identifier: MIT

package webui

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// The route exists, is reached through the real middleware chain, and hands the
// daemon the pair the request names.
//
// A ROUTE THAT ANSWERS 503 LOOKS EXACTLY LIKE A WORKING ONE until somebody
// presses the button. The reflection test in the daemon catches a nil action;
// this catches the other half — a path that was never registered, or registered
// under a name the page does not use.
func TestThePauseRouteReachesTheDaemon(t *testing.T) {
	for _, body := range []string{
		`{"target":"laptopcard","paused":true}`,
		`{"target":"laptopcard","paused":false}`,
	} {
		d := newDashboard(t)
		rec := d.do(http.MethodPost, "/api/folders/kqz3d-8xh2p/paused", body, fromThePage(d.base))
		if rec.Code != http.StatusOK {
			t.Fatalf("%s got %d: %s", body, rec.Code, rec.Body.String())
		}
		if d.calls != 1 {
			t.Errorf("%s did not reach the daemon (%d calls)", body, d.calls)
		}
	}
}

// The folder is in the path and the destination in the body, and BOTH have to
// arrive: a folder with three destinations has three of these switches, and a
// dropped target would stop backing up whichever one the daemon guessed.
func TestPausingCarriesBothTheFolderAndTheDestination(t *testing.T) {
	for _, want := range []bool{true, false} {
		s := &Server{}
		var gotID, gotTarget string
		var gotPaused bool
		s.actions.SetMirrorPaused = func(folderID, target string, paused bool) error {
			gotID, gotTarget, gotPaused = folderID, target, paused
			return nil
		}
		body := `{"target":"laptopcard","paused":false}`
		if want {
			body = `{"target":"laptopcard","paused":true}`
		}
		req := httptest.NewRequest(http.MethodPost, "/api/folders/kqz3d-8xh2p/paused", strings.NewReader(body))
		req.SetPathValue("id", "kqz3d-8xh2p")
		rec := httptest.NewRecorder()
		s.handleSetMirrorPaused(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("got %d: %s", rec.Code, rec.Body.String())
		}
		if gotID != "kqz3d-8xh2p" || gotTarget != "laptopcard" || gotPaused != want {
			t.Errorf("daemon was asked for %q → %q paused=%v, want paused=%v", gotID, gotTarget, gotPaused, want)
		}
		// And the answer says what happened to the files, because that is the
		// question anybody pressing this actually has.
		if want && !strings.Contains(rec.Body.String(), "stays exactly where it is") {
			t.Errorf("the reply does not promise the existing backup is untouched: %s", rec.Body.String())
		}
	}
}

// A refusal the daemon makes comes back as a 422 with its sentence intact, so
// the page can show the reason rather than "that did not work".
func TestPausingPassesTheDaemonsRefusalBack(t *testing.T) {
	s := &Server{}
	s.actions.SetMirrorPaused = func(string, string, bool) error {
		return errors.New(`nothing copies photos to "laptopcard" continuously`)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/folders/kqz3d-8xh2p/paused",
		strings.NewReader(`{"target":"laptopcard","paused":true}`))
	req.SetPathValue("id", "kqz3d-8xh2p")
	rec := httptest.NewRecorder()
	s.handleSetMirrorPaused(rec, req)

	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("got %d, want 422", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "nothing copies") {
		t.Errorf("the reason was lost: %q", rec.Body.String())
	}
}

// THE GUARANTEE: the switch is not reachable from the read-only network view.
//
// That view is unauthenticated by design — anyone on the wifi can watch — so a
// route that STOPS somebody's backups must never be on it. It is an allow-list,
// so this is denied by the same rule that denies everything else; the test
// exists because "denied by default" holds only while nobody adds it to the
// list.
func TestPausingIsRefusedOnTheReadOnlyNetworkView(t *testing.T) {
	code, reached := probe(t, http.MethodPost, "/api/folders/kqz3d-8xh2p/paused")
	if reached {
		t.Error("POST /api/folders/{id}/paused REACHED the handler from the network view")
	}
	if code != http.StatusForbidden {
		t.Errorf("POST /api/folders/{id}/paused returned %d, want 403", code)
	}
}

// ...but a reader of that view is still TOLD that a mirror is paused. It is a
// health fact of exactly the kind the view exists to publish — "this folder is
// not being copied to that destination right now" — and it names no path, no
// address and no capacity. Somebody in another room who sees "backed up" for a
// destination that is switched off has been misinformed by us.
func TestTheNetworkViewStillSaysAMirrorIsPaused(t *testing.T) {
	full := map[string]any{
		"machine_name": "my-laptop",
		"rows": []any{
			map[string]any{"folder_label": "photos", "target_name": "sdcard",
				"state": "paused", "paused": true, "folder_path": "/home/alex/photos"},
		},
	}
	var reached bool
	h := lanReadOnlyOpen(everythingHandler(&reached), func() any { return full })
	req := httptest.NewRequest(http.MethodGet, "/api/status", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	body := rec.Body.String()
	if !strings.Contains(body, `"paused":true`) || !strings.Contains(body, `"state":"paused"`) {
		t.Errorf("the network view no longer says a mirror is paused, so it reads as backed up: %s", body)
	}
	if strings.Contains(body, "/home/alex/photos") {
		t.Error("the row's folder path leaked; only the health facts belong on this view")
	}
}
