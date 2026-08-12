// SPDX-License-Identifier: MIT

package webui

import (
	"net/http"
	"testing"
)

// The listing route reaches the daemon with the destination and the directory
// asked for. Everything the view decides is decided there; this proves the
// question gets through unmangled.
func TestBrowsingADestinationReachesTheDaemon(t *testing.T) {
	d := newDashboard(t)
	var gotTarget, gotPath string
	d.actions.DestFiles = func(target, path string) (any, error) {
		gotTarget, gotPath = target, path
		return map[string]any{"target": target, "path": path}, nil
	}

	rec := d.do(http.MethodGet, "/api/targets/laptopcard/files?path=my-laptop%2FDevelopment", "",
		map[string]string{"Authorization": "Bearer the-token"})
	if rec.Code != http.StatusOK {
		t.Fatalf("got %d (%s), want 200", rec.Code, rec.Body.String())
	}
	if gotTarget != "laptopcard" || gotPath != "my-laptop/Development" {
		t.Errorf("the daemon was asked for %q on %q", gotPath, gotTarget)
	}
}

// The delete route carries the path and the typed name to the daemon, which is
// where both are checked.
func TestDeletingFromADestinationReachesTheDaemon(t *testing.T) {
	d := newDashboard(t)
	var gotTarget, gotPath, gotConfirm string
	d.actions.DeleteDestFile = func(target, path, confirm string) (any, error) {
		gotTarget, gotPath, gotConfirm = target, path, confirm
		return map[string]any{"ok": true}, nil
	}

	rec := d.do(http.MethodPost, "/api/targets/laptopcard/files/delete",
		`{"path":"backup-maker-archives/my-laptop/nightly","confirm":"nightly"}`,
		map[string]string{"Authorization": "Bearer the-token", MutationHeader: "1"})
	if rec.Code != http.StatusOK {
		t.Fatalf("got %d (%s), want 200", rec.Code, rec.Body.String())
	}
	if gotTarget != "laptopcard" || gotPath != "backup-maker-archives/my-laptop/nightly" || gotConfirm != "nightly" {
		t.Errorf("the daemon was asked to delete %q on %q, confirmed with %q", gotPath, gotTarget, gotConfirm)
	}
}

// An empty confirmation never reaches the daemon at all — and the daemon
// refuses it too, which is the check that counts.
func TestDeletingFromADestinationNeedsATypedName(t *testing.T) {
	d := newDashboard(t)
	reached := false
	d.actions.DeleteDestFile = func(string, string, string) (any, error) {
		reached = true
		return nil, nil
	}

	rec := d.do(http.MethodPost, "/api/targets/laptopcard/files/delete",
		`{"path":"backup-maker-archives/my-laptop/nightly","confirm":"  "}`,
		map[string]string{"Authorization": "Bearer the-token", MutationHeader: "1"})
	if rec.Code != http.StatusUnprocessableEntity {
		t.Errorf("got %d, want 422", rec.Code)
	}
	if reached {
		t.Error("a delete with no typed name reached the daemon")
	}
}

// A nil action is a 503, not a panic — the failure this project has shipped
// twice. TestEveryActionIsWired in the daemon is the other half.
func TestTheFileViewRoutesReportBeingUnwired(t *testing.T) {
	d := newDashboard(t)
	d.actions.DestFiles = nil
	d.actions.DeleteDestFile = nil

	for _, c := range []struct{ method, path, body string }{
		{http.MethodGet, "/api/targets/laptopcard/files", ""},
		{http.MethodPost, "/api/targets/laptopcard/files/delete", `{"path":"x/y","confirm":"y"}`},
	} {
		rec := d.do(c.method, c.path, c.body,
			map[string]string{"Authorization": "Bearer the-token", MutationHeader: "1"})
		if rec.Code != http.StatusServiceUnavailable {
			t.Errorf("%s %s got %d, want 503", c.method, c.path, rec.Code)
		}
	}
}

// Both routes are behind the token, like every other route on the loopback
// dashboard. The listing describes where somebody's files are filed; the
// delete removes them.
func TestTheFileViewRoutesNeedAToken(t *testing.T) {
	for _, c := range []struct{ method, path, body string }{
		{http.MethodGet, "/api/targets/laptopcard/files?path=my-laptop", ""},
		{http.MethodPost, "/api/targets/laptopcard/files/delete", `{"path":"x/y","confirm":"y"}`},
	} {
		d := newDashboard(t)
		rec := d.do(c.method, c.path, c.body, map[string]string{MutationHeader: "1"})
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("%s %s with no token got %d, want 401", c.method, c.path, rec.Code)
		}
		if d.calls != 0 {
			t.Errorf("%s %s with no token REACHED the daemon", c.method, c.path)
		}
	}
}
