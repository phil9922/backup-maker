// SPDX-License-Identifier: MIT

package webui

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// The routes exist, are reached through the real middleware chain, and hand the
// daemon the pair or the schedule the path names.
//
// A ROUTE THAT ANSWERS 503 LOOKS EXACTLY LIKE A WORKING ONE until somebody
// presses the button, which this project has shipped twice; the reflection test
// in the daemon catches a nil action, and this catches the other half — a path
// that was never registered, or registered under a name the page does not use.
func TestBackUpNowRoutesReachTheDaemon(t *testing.T) {
	d := newDashboard(t)
	rec := d.do(http.MethodPost, "/api/folders/kqz3d-8xh2p/backup-now",
		`{"target":"laptopcard"}`, fromThePage(d.base))
	if rec.Code != http.StatusOK {
		t.Fatalf("backing up a folder now got %d: %s", rec.Code, rec.Body.String())
	}
	if d.calls != 1 {
		t.Errorf("the request did not reach the daemon (%d calls)", d.calls)
	}
	// The answer says what has STARTED. A body that read as "backed up" would be
	// a lie for the several minutes a share takes.
	if !strings.Contains(rec.Body.String(), "Checking") {
		t.Errorf("the reply does not say what began: %s", rec.Body.String())
	}

	d = newDashboard(t)
	rec = d.do(http.MethodPost, "/api/archives/nightly/backup-now", ``, fromThePage(d.base))
	if rec.Code != http.StatusOK {
		t.Fatalf("writing a snapshot now got %d: %s", rec.Code, rec.Body.String())
	}
	if d.calls != 1 {
		t.Errorf("the request did not reach the daemon (%d calls)", d.calls)
	}
}

// The folder is in the path and the destination in the body, and BOTH have to
// arrive: a folder with three destinations has three of these buttons, and a
// dropped target would run whichever one the daemon guessed.
func TestBackUpFolderNowCarriesTheDestination(t *testing.T) {
	s := &Server{}
	var gotID, gotTarget string
	s.actions.BackUpFolderNow = func(folderID, target string) (any, error) {
		gotID, gotTarget = folderID, target
		return map[string]any{"ok": true}, nil
	}
	req := httptest.NewRequest(http.MethodPost, "/api/folders/kqz3d-8xh2p/backup-now",
		strings.NewReader(`{"target":"laptopcard"}`))
	req.SetPathValue("id", "kqz3d-8xh2p")
	rec := httptest.NewRecorder()
	s.handleBackUpFolderNow(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("got %d: %s", rec.Code, rec.Body.String())
	}
	if gotID != "kqz3d-8xh2p" || gotTarget != "laptopcard" {
		t.Errorf("daemon was asked for folder %q → %q", gotID, gotTarget)
	}
}

// A refusal the daemon makes — paused, no password, unreachable destination —
// comes back as a 422 with its sentence intact, so the page can show the reason
// rather than "that did not work".
func TestBackUpNowPassesTheDaemonsRefusalBack(t *testing.T) {
	s := &Server{}
	s.actions.BackUpArchiveNow = func(string) (any, error) {
		return nil, errors.New(`"nightly" is paused. Resume it first`)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/archives/nightly/backup-now", nil)
	req.SetPathValue("name", "nightly")
	rec := httptest.NewRecorder()
	s.handleBackUpArchiveNow(rec, req)

	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("got %d, want 422", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "paused") {
		t.Errorf("the reason was lost: %q", rec.Body.String())
	}
}

// THE GUARANTEE. Neither button, and neither route, is reachable from the
// read-only network view.
//
// The view is unauthenticated by design — anyone on the wifi can watch — so a
// route that starts gigabytes of work must never be on it. It is an allow-list,
// so these are denied by the same rule that denies everything else; the test
// exists because "denied by default" is only true for as long as nobody adds
// them to the list.
func TestBackUpNowIsRefusedOnTheReadOnlyNetworkView(t *testing.T) {
	for _, path := range []string{
		"/api/folders/kqz3d-8xh2p/backup-now",
		"/api/archives/nightly/backup-now",
	} {
		code, reached := probe(t, http.MethodPost, path)
		if reached {
			t.Errorf("POST %s REACHED the handler from the network view", path)
		}
		if code != http.StatusForbidden {
			t.Errorf("POST %s returned %d, want 403", path, code)
		}
	}
}
