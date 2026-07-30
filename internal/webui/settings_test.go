// SPDX-License-Identifier: MIT

package webui

import (
	"encoding/json"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/phil9922/backup-maker/internal/status"
)

// THE SHARPEST CASE ON THIS ROUTE. One of the settings it writes is whether the
// unauthenticated network view exists at all, and another is whether the owner
// is told when their backups stop. A view that could reach this would be able
// to switch itself on, or switch the warnings off — from any device on the
// wifi, with no password. It must be refused as flatly as every other write.
func TestTheNetworkViewCannotChangeSettings(t *testing.T) {
	for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodDelete} {
		code, reached := probe(t, method, "/api/settings")
		if reached {
			t.Errorf("%s /api/settings reached the real handler from the network view", method)
		}
		if code != http.StatusForbidden {
			t.Errorf("%s /api/settings returned %d, want 403", method, code)
		}
	}
}

// And it is not TOLD them either. Which alerts a household has switched off
// describes how closely they are watching — useful to nobody in another room,
// and useful to somebody working out whether a fault here would be noticed.
// The LAN view's own URL is in there too, which its readers plainly do not need.
func TestNetworkStatusHidesTheSettings(t *testing.T) {
	m := status.Model{
		MachineName: "workstation",
		Settings: status.Settings{
			DesktopAlerts:  false,
			BackupsStopped: false,
			LANView:        true,
			LANViewURL:     "http://192.168.1.24:8667",
		},
	}
	raw, err := json.Marshal(RedactForNetwork(m))
	if err != nil {
		t.Fatal(err)
	}
	got := string(raw)
	if strings.Contains(got, "settings") {
		t.Errorf("the settings block was published to the network view:\n%s", got)
	}
	for _, leak := range []string{"desktop_alerts", "alert_backups_stopped", "192.168.1.24"} {
		if strings.Contains(got, leak) {
			t.Errorf("%q reached the network view:\n%s", leak, got)
		}
	}
	// The health facts it exists to show must still be there, or this test
	// would pass just as well against an empty document.
	if !strings.Contains(got, "workstation") {
		t.Errorf("redaction removed the machine name as well:\n%s", got)
	}
}

// A partial update must leave the settings it does not mention alone. With
// plain bools on the request, a client sending only the box it clicked would
// switch off every alert it omitted — which is exactly how a user ends up
// silently unprotected after touching something unrelated.
func TestASettingsRequestOnlyCarriesWhatWasSent(t *testing.T) {
	var req SettingsRequest
	if err := json.Unmarshal([]byte(`{"alert_snapshot_failed":false}`), &req); err != nil {
		t.Fatal(err)
	}
	if req.SnapshotFailed == nil || *req.SnapshotFailed {
		t.Error("the field that was sent did not arrive as an explicit false")
	}
	for name, got := range map[string]*bool{
		"desktop_alerts":             req.DesktopAlerts,
		"alert_backups_stopped":      req.BackupsStopped,
		"alert_unrecognised_storage": req.UnrecognisedStorage,
		"alert_pair_requests":        req.PairRequests,
		"lan_view":                   req.LANView,
	} {
		if got != nil {
			t.Errorf("%s was not sent but arrived as %v — an unrelated setting would be overwritten", name, *got)
		}
	}
}

// THE UPGRADE THAT APPEARS NOT TO HAVE HAPPENED. embed.FS carries no
// modification times, so net/http served the dashboard's own HTML, CSS and
// JavaScript with no Last-Modified, no ETag and no Cache-Control at all. With
// nothing to revalidate against, the browser keeps what it has — so a user
// upgrades, restarts the daemon, reloads, and is still running the old
// dashboard. Every UI fix would look like it had not shipped, which is exactly
// how this was found: a feature that was demonstrably in the served HTML was
// reported missing from the screen.
func TestDashboardAssetsCanBeRevalidated(t *testing.T) {
	h := staticAssets(mustStatic(t))

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/app.js", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("serving app.js returned %d", rec.Code)
	}
	tag := rec.Header().Get("ETag")
	if tag == "" {
		t.Fatal("no ETag: the browser has nothing to revalidate against and will keep a stale dashboard")
	}
	if cc := rec.Header().Get("Cache-Control"); cc != "no-cache" {
		t.Errorf("Cache-Control = %q, want no-cache — the browser must ask before reusing it", cc)
	}

	// The point of the tag: unchanged builds cost nothing.
	fresh := httptest.NewRequest(http.MethodGet, "/app.js", nil)
	fresh.Header.Set("If-None-Match", tag)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, fresh)
	if rec.Code != http.StatusNotModified {
		t.Errorf("a matching ETag returned %d, want 304", rec.Code)
	}

	// And the half that actually matters: a browser holding an older build's
	// copy must be given the new one, not told to keep what it has.
	stale := httptest.NewRequest(http.MethodGet, "/app.js", nil)
	stale.Header.Set("If-None-Match", `"some-older-build"`)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, stale)
	if rec.Code != http.StatusOK {
		t.Errorf("an ETag from an older build returned %d, want 200 — the upgrade would never reach the screen", rec.Code)
	}
}

// mustStatic is the embedded dashboard tree, rooted the same way the server
// roots it.
func mustStatic(t *testing.T) fs.FS {
	t.Helper()
	sub, err := fs.Sub(staticFS, "static")
	if err != nil {
		t.Fatal(err)
	}
	return sub
}

// The build fingerprint is stripped from the network view for the same reason
// the version is: it tells an unauthenticated reader exactly which known bugs
// apply to this machine.
func TestNetworkStatusHidesTheBuildCommit(t *testing.T) {
	m := status.Model{MachineName: "workstation", Version: "dev-dirty", Commit: "a170cc6"}
	raw, err := json.Marshal(RedactForNetwork(m))
	if err != nil {
		t.Fatal(err)
	}
	got := string(raw)
	for _, leak := range []string{"a170cc6", "dev-dirty"} {
		if strings.Contains(got, leak) {
			t.Errorf("%q reached the network view: %s", leak, got)
		}
	}
	if !strings.Contains(got, "workstation") {
		t.Errorf("redaction removed the machine name too: %s", got)
	}
}
