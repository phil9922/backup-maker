// SPDX-License-Identifier: MIT

package webui

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/phil9922/backup-maker/internal/status"
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
		// Approving another machine lets it write into this one's receive
		// folder. Strictly a decision made at this computer.
		{http.MethodPost, "/api/pair/accept"},
		// The QR encodes this machine's device ID, which the network view
		// redacts from /api/status for the same reason: it is identity, and a
		// picture of it is no less so.
		{http.MethodGet, "/api/pair/qr"},
		// Reverting DELETES files that exist only on this computer. The view
		// publishes that a received backup has drifted, deliberately — undoing
		// it is a decision made at the machine itself, never from a phone.
		{http.MethodPost, "/api/receive/revert"},
		{http.MethodPost, "/api/targets/share"},
		{http.MethodPost, "/api/setup/complete"},
		{http.MethodDelete, "/api/folders/abc"},
		{http.MethodPost, "/api/folders/abc/ignores"},
		{http.MethodDelete, "/api/targets/nas"},
		// Adoption reads destination manifests and rewrites the whole
		// configuration; strictly a loopback operation.
		{http.MethodGet, "/api/adopt/scan"},
		{http.MethodPost, "/api/adopt/inspect"},
		{http.MethodPost, "/api/adopt/test-share"},
		{http.MethodPost, "/api/adopt"},
		{http.MethodPost, "/api/archives/weekly/password"},
		// The event stream serves the unredacted snapshot and holds a
		// connection open; the network view polls the redacted status instead.
		{http.MethodGet, "/api/events"},
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

// Capacity is stripped, but "this destination's free space cannot be read, so
// its reserve is not being enforced" is health, not hardware — it names no
// path, address or size. Someone watching from another room is owed it, or the
// network view shows an unprotected destination as a healthy one.
func TestNetworkStatusKeepsUnknownSpaceWarning(t *testing.T) {
	full := map[string]any{
		"machine_name": "workstation",
		"targets": []any{
			map[string]any{"name": "nas", "state": "in sync", "location": "//192.168.1.50/backups",
				"space_unknown": true},
		},
	}
	var reached bool
	h := lanReadOnly(everythingHandler(&reached), func() any { return full })
	req := httptest.NewRequest(http.MethodGet, "/api/status", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	body := rec.Body.String()
	if !strings.Contains(body, `"space_unknown":true`) {
		t.Errorf("redaction dropped the unprotected-destination warning: %s", body)
	}
	if strings.Contains(body, "192.168.1.50") {
		t.Error("network status leaked the destination's address")
	}
}

// Redaction strips named keys, never states — and "this machine is waiting to
// be approved at the other end" is health, not a description of the hardware.
// Someone watching from another room needs to know a destination has never
// actually connected.
func TestNetworkStatusKeepsAwaitingPairState(t *testing.T) {
	full := map[string]any{
		"machine_name": "workstation",
		"targets": []any{
			map[string]any{"name": "attic-pi", "state": "awaiting-pair", "location": "XKQ4TZ2…"},
		},
		"rows": []any{
			map[string]any{"folder_label": "code", "target_name": "attic-pi", "state": "awaiting-pair"},
		},
	}
	var reached bool
	h := lanReadOnly(everythingHandler(&reached), func() any { return full })
	req := httptest.NewRequest(http.MethodGet, "/api/status", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if strings.Count(rec.Body.String(), "awaiting-pair") != 2 {
		t.Errorf("redaction dropped the awaiting-pair state: %s", rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "XKQ4TZ2") {
		t.Error("network status leaked the destination's device ID")
	}
}

// The received-backup panel is published to the network, and it must publish
// only health: WHICH backups this machine holds and whether any of them have
// been changed here. Where they live on disk is the same secret as receive.root
// — which is redacted a few lines above — so it must not come back per folder.
//
// Asserted on the marshalled payload of the REAL model rather than the struct:
// a path added as a field later would be invisible to a field-by-field check
// but would sail straight onto the wifi.
func TestNetworkStatusPublishesReceivedFoldersWithoutTheirPaths(t *testing.T) {
	m := status.Model{
		MachineName: "attic-pi",
		Receive:     status.ReceiveInfo{Enabled: true, Root: "/srv/backups"},
		ReceivedFolders: []status.ReceivedFolderInfo{
			{ID: "f1", Label: "code", Source: "workstation", ChangedItems: 3, ChangedBytes: 37},
			{ID: "f2", Label: "photos", Source: "workstation"},
		},
	}

	var reached bool
	h := lanReadOnly(everythingHandler(&reached), func() any { return m })
	req := httptest.NewRequest(http.MethodGet, "/api/status", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	body := rec.Body.String()
	// Not the root, and not any path built from it — the folders land in
	// <root>/<machine>/<label>, so one leaked path gives away the lot.
	for _, secret := range []string{"/srv", "srv/backups", "\\srv"} {
		if strings.Contains(body, secret) {
			t.Errorf("network status leaked where received backups are stored (%q): %s", secret, body)
		}
	}
	// And it is still worth publishing: a backup that no longer matches the
	// machine it protects is exactly what someone watching should see.
	for _, want := range []string{`"label":"code"`, `"changed_items":3`, `"source":"workstation"`} {
		if !strings.Contains(body, want) {
			t.Errorf("network status dropped %s, which is health, not hardware: %s", want, body)
		}
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

// A machine waiting to be approved carries the very device ID the redaction
// deletes at the top level, plus the address it dialled in from — and it is not
// even an approved peer yet. Publishing the list would hand back on one line
// what the rest of this function spends its effort withholding.
func TestNetworkStatusHidesWhoIsWaitingToPair(t *testing.T) {
	full := map[string]any{
		"machine_name": "workstation",
		"pending_sources": []any{
			map[string]any{
				"device_id": "PPPPPPP-QQQQQQQ-RRRRRRR",
				"name":      "phils-laptop",
				"address":   "tcp://192.168.1.42:22000",
			},
		},
		"archives": []any{
			map[string]any{
				"name": "monthly", "state": "error",
				"detail": `cannot decrypt taxes/2026-return.pdf: wrong password`,
			},
		},
	}

	var reached bool
	h := lanReadOnly(everythingHandler(&reached), func() any { return full })
	req := httptest.NewRequest(http.MethodGet, "/api/status", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	body := rec.Body.String()

	for _, secret := range []string{
		"PPPPPPP-QQQQQQQ-RRRRRRR", // identity of a machine that is not even approved
		"phils-laptop",            // whose machine it is
		"192.168.1.42",            // and where it is
		"taxes/2026-return.pdf",   // a real filename, from a failed snapshot
	} {
		if strings.Contains(body, secret) {
			t.Errorf("network status leaked %q", secret)
		}
	}

	// The fact that SOMETHING is asking is health worth seeing from another
	// room; who and where is a decision for the dashboard.
	if !strings.Contains(body, `"pending_source_count":1`) {
		t.Errorf("network status should still report how many are waiting; got %s", body)
	}
	// The archive row itself survives — only its detail line goes.
	if !strings.Contains(body, "monthly") {
		t.Error("archive health should still be visible")
	}
}

// The lifetime volume figure measures the household, not a destination's
// health: how much data lives here and how fast it churns. Nobody watching
// from another room can act on it, and capacity is stripped from every target
// for the same reason.
func TestNetworkStatusHidesTheLifetimeTotal(t *testing.T) {
	full := map[string]any{
		"machine_name": "workstation",
		"version":      "0.1.2",
		"totals": map[string]any{
			"bytes_copied": 1503238553600.0,
			"files_copied": 82391.0,
		},
	}

	var reached bool
	h := lanReadOnly(everythingHandler(&reached), func() any { return full })
	req := httptest.NewRequest(http.MethodGet, "/api/status", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	body := rec.Body.String()

	if strings.Contains(body, "0.1.2") || strings.Contains(body, `"version"`) {
		t.Errorf("network status leaked the running version: %s", body)
	}
	if strings.Contains(body, "bytes_copied") || strings.Contains(body, "82391") {
		t.Errorf("network status leaked the lifetime total: %s", body)
	}
	if !strings.Contains(body, "workstation") {
		t.Error("the view still has to be useful")
	}
}
