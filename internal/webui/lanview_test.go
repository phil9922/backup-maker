// SPDX-License-Identifier: MIT

package webui

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// everythingHandler stands in for the real mux: if a request reaches it, the
// read-only wrapper let it through.
func everythingHandler(reached *bool) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*reached = true
		w.WriteHeader(http.StatusOK)
	})
}

func probe(t *testing.T, method, path string) (code int, reached bool) {
	t.Helper()
	h := lanReadOnly(everythingHandler(&reached), func() any { return map[string]any{} })
	req := httptest.NewRequest(method, path, strings.NewReader("{}"))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec.Code, reached
}

// The whole point of the LAN view: watching is allowed, changing is not.
func TestLANViewAllowsOnlyReading(t *testing.T) {
	allowed := []struct{ method, path string }{
		{http.MethodGet, "/"},
		{http.MethodGet, "/app.js"},
		{http.MethodGet, "/style.css"},
	}
	for _, c := range allowed {
		code, reached := probe(t, c.method, c.path)
		if code != http.StatusOK || !reached {
			t.Errorf("%s %s was blocked (%d); the view must still show status", c.method, c.path, code)
		}
	}
}

// Every route that changes configuration, reads the filesystem, or touches the
// network must be refused — enforced by an allow-list, so a route added later
// is denied by default rather than silently exposed.
func TestLANViewRefusesEverythingElse(t *testing.T) {
	blocked := []struct{ method, path string }{
		// Filesystem enumeration. Its entire security justification is that
		// any caller already runs as this user; that dies on the network.
		{http.MethodGet, "/api/browse"},
		{http.MethodGet, "/api/machines"},
		{http.MethodPost, "/api/machines/storage"},
		{http.MethodPost, "/api/backups"},
		{http.MethodPost, "/api/archives"},
		{http.MethodPost, "/api/scan"},
		{http.MethodPost, "/api/wake"},
		{http.MethodPost, "/api/targets/share"},
		{http.MethodPost, "/api/setup/complete"},
		{http.MethodDelete, "/api/folders/abc"},
		{http.MethodDelete, "/api/targets/nas"},
		// The token exchange must never happen over the network.
		{http.MethodGet, "/auth"},
		// Not a real route today — proves the allow-list denies by default.
		{http.MethodGet, "/api/something-added-next-year"},
		{http.MethodPost, "/api/status"},
	}
	for _, c := range blocked {
		code, reached := probe(t, c.method, c.path)
		if reached {
			t.Errorf("%s %s REACHED the handler; it must never be callable from the network", c.method, c.path)
		}
		if code != http.StatusForbidden {
			t.Errorf("%s %s returned %d, want 403", c.method, c.path, code)
		}
	}
}

// The view answers ping itself so the page can tell it is read-only and hide
// controls, rather than showing buttons that 403 when tapped.
func TestLANViewAnnouncesItselfAsReadOnly(t *testing.T) {
	var reached bool
	h := lanReadOnly(everythingHandler(&reached), func() any { return map[string]any{} })
	req := httptest.NewRequest(http.MethodGet, "/api/ping", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("ping returned %d, want 200", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), `"read_only":true`) {
		t.Errorf("ping body %q must flag the view as read-only", rec.Body.String())
	}
}

// The refusal has to explain itself, or a user on a phone just sees a dead
// button and assumes the software is broken.
func TestLANViewExplainsWhyItRefused(t *testing.T) {
	var reached bool
	h := lanReadOnly(everythingHandler(&reached), func() any { return map[string]any{} })
	req := httptest.NewRequest(http.MethodPost, "/api/backups", strings.NewReader("{}"))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	body := rec.Body.String()
	if !strings.Contains(body, "read-only") || !strings.Contains(body, "computer running backup-maker") {
		t.Errorf("unhelpful refusal message: %q", body)
	}
}

// Binding 0.0.0.0 would also expose the dashboard on VPN and container
// interfaces the user never intended to include.
func TestCheckBindableRejectsNonsense(t *testing.T) {
	if err := checkBindable("not-an-ip", 8667); err == nil {
		t.Error("a non-address was accepted as a bind target")
	}
	if err := checkBindable("192.168.1.10", 0); err == nil {
		t.Error("port 0 was accepted for a view users must be able to bookmark")
	}
	if err := checkBindable("192.168.1.10", 70000); err == nil {
		t.Error("an out-of-range port was accepted")
	}
	if err := checkBindable("192.168.1.10", 8667); err != nil {
		t.Errorf("a valid address/port was rejected: %v", err)
	}
}

// "Are my backups working?" is fine to publish to the network. "Here is my
// filesystem layout and where my NAS lives" is reconnaissance, and would be
// handed to every phone, TV and smart device on the wifi.
func TestNetworkStatusHidesPathsAndAddresses(t *testing.T) {
	full := map[string]any{
		"machine_name": "workstation",
		"device_id":    "AAAAAAA-BBBBBBB-CCCCCCC",
		"receive":      map[string]any{"enabled": true, "root": "/mnt/backups/incoming"},
		"folders": []any{
			map[string]any{"id": "f1", "label": "code", "path": "/home/alex/code"},
		},
		"targets": []any{
			map[string]any{"name": "nas", "state": "in sync", "location": "//192.168.1.50/backups",
				"free_bytes": 335007449088.0, "total_bytes": 1979120929792.0,
				"space_reported_at": "2026-07-24T10:00:00Z", "min_free_bytes": 21474836480.0},
		},
		"rows": []any{
			map[string]any{"folder_label": "code", "target_name": "nas",
				"state": "syncing", "completion": 64.0, "folder_path": "/home/alex/code"},
		},
	}

	var reached bool
	h := lanReadOnly(everythingHandler(&reached), func() any { return full })
	req := httptest.NewRequest(http.MethodGet, "/api/status", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	body := rec.Body.String()
	if rec.Code != http.StatusOK {
		t.Fatalf("status returned %d, want 200 — any device should be able to look", rec.Code)
	}

	// Must not leak.
	for _, secret := range []string{
		"/home/alex/code",         // folder path
		"//192.168.1.50/backups",  // where the NAS lives
		"/mnt/backups/incoming",   // receive root
		"AAAAAAA-BBBBBBB-CCCCCCC", // device identity
		"free_bytes",              // how full the hardware is
		"total_bytes",             // how big the hardware is
		"space_reported_at",
		"min_free_bytes",
	} {
		if strings.Contains(body, secret) {
			t.Errorf("network status leaked %q", secret)
		}
	}

	// Must still be useful.
	for _, want := range []string{"workstation", "code", "nas", "syncing"} {
		if !strings.Contains(body, want) {
			t.Errorf("network status is missing %q; it has to still show health", want)
		}
	}
	if !strings.Contains(body, `"redacted":true`) {
		t.Error("payload should mark itself redacted so the UI can say so")
	}
}

// Redaction must not depend on a token: the whole point is that any device on
// the network can glance at it.
func TestNetworkStatusNeedsNoToken(t *testing.T) {
	var reached bool
	h := lanReadOnly(everythingHandler(&reached), func() any {
		return map[string]any{"machine_name": "workstation"}
	})
	req := httptest.NewRequest(http.MethodGet, "/api/status", nil) // no cookie, no bearer
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Errorf("unauthenticated status returned %d, want 200", rec.Code)
	}
	if reached {
		t.Error("the network view must serve its own redacted status, not the full handler")
	}
}
