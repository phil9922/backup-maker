// SPDX-License-Identifier: MIT

package webui

import (
	"bytes"
	"errors"
	"fmt"
	"image/png"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	qrcode "github.com/skip2/go-qrcode"

	"github.com/phil9922/backup-maker/internal/config"
)

func okHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
}

// The listener already binds loopback; this guard is what stops a DNS-rebinding
// page from talking to the dashboard under its own hostname.
func TestLoopbackOnlyRejectsForeignHosts(t *testing.T) {
	h := loopbackOnly(okHandler())

	allowed := []string{"127.0.0.1:8666", "localhost:8666", "127.0.0.1", "localhost", "[::1]:8666"}
	for _, host := range allowed {
		req := httptest.NewRequest(http.MethodGet, "/api/status", nil)
		req.Host = host
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Errorf("host %q was rejected with %d; it should be allowed", host, rec.Code)
		}
	}

	blocked := []string{"evil.example.com", "evil.example.com:8666", "192.168.1.50:8666", "backup-maker.attacker.test"}
	for _, host := range blocked {
		req := httptest.NewRequest(http.MethodGet, "/api/status", nil)
		req.Host = host
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusForbidden {
			t.Errorf("host %q got %d; it should be forbidden", host, rec.Code)
		}
	}
}

// Editing excludes from the dashboard has to reach the same place the CLI
// writes to: the folder id from the path, the patterns from the body, and the
// "back up junk too" flag round-tripped rather than reset.
func TestSetFolderIgnoresPassesThrough(t *testing.T) {
	s := &Server{}
	var gotID string
	var gotPatterns []string
	var gotNoDefaults bool
	s.actions.SetFolderIgnores = func(id string, extraIgnore []string, noDefaultIgnores bool) error {
		gotID, gotPatterns, gotNoDefaults = id, extraIgnore, noDefaultIgnores
		return nil
	}

	body := `{"ignores":["scratch","*.iso"],"no_default_ignores":true}`
	req := httptest.NewRequest(http.MethodPost, "/api/folders/kqz3d-8xh2p/ignores", strings.NewReader(body))
	req.SetPathValue("id", "kqz3d-8xh2p")
	rec := httptest.NewRecorder()
	s.handleSetFolderIgnores(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("got %d: %s", rec.Code, rec.Body.String())
	}
	if gotID != "kqz3d-8xh2p" {
		t.Errorf("folder id = %q, want the one from the path", gotID)
	}
	if len(gotPatterns) != 2 || gotPatterns[0] != "scratch" || gotPatterns[1] != "*.iso" {
		t.Errorf("patterns = %v", gotPatterns)
	}
	if !gotNoDefaults {
		t.Error("no_default_ignores was dropped; the folder would silently start skipping junk again")
	}
}

// An unknown folder is a 422, not a 500: the request was well-formed, the
// machine just has nothing by that id (a stale tab, or a folder removed in
// another window).
func TestSetFolderIgnoresRejectsUnknownFolder(t *testing.T) {
	s := &Server{}
	s.actions.SetFolderIgnores = func(id string, extraIgnore []string, noDefaultIgnores bool) error {
		return errors.New("no folder with id \"gone\"")
	}
	req := httptest.NewRequest(http.MethodPost, "/api/folders/gone/ignores", strings.NewReader(`{"ignores":[]}`))
	req.SetPathValue("id", "gone")
	rec := httptest.NewRecorder()
	s.handleSetFolderIgnores(rec, req)

	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("got %d, want 422", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "no folder with id") {
		t.Errorf("unhelpful error body: %q", rec.Body.String())
	}
}

// The Approve button's whole job: hand the full device ID to the daemon, which
// is the only thing that knows what is actually waiting to pair.
func TestAcceptPairPassesTheDeviceIDThrough(t *testing.T) {
	s := &Server{}
	got := ""
	s.actions.AcceptPair = func(deviceID string) error {
		got = deviceID
		return nil
	}
	const id = "ABCDEFG-HIJKLMN-OPQRSTU-VWXYZ01-2345678-9ABCDEF-GHIJKLM-NOPQRST"
	req := httptest.NewRequest(http.MethodPost, "/api/pair/accept",
		strings.NewReader(`{"device_id":"`+id+`"}`))
	rec := httptest.NewRecorder()
	s.handleAcceptPair(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("got %d: %s", rec.Code, rec.Body.String())
	}
	if got != id {
		t.Errorf("device id = %q, want the full ID from the body", got)
	}
}

// An ID nobody is waiting on must never be added to the approved list: that
// list is what lets another machine write here, so it can only ever confirm a
// request that was already made.
func TestAcceptPairRejectsADeviceThatIsNotWaiting(t *testing.T) {
	s := &Server{}
	called := false
	s.actions.AcceptPair = func(deviceID string) error {
		called = true
		return errors.New("no machine with that ID is waiting for approval")
	}
	req := httptest.NewRequest(http.MethodPost, "/api/pair/accept",
		strings.NewReader(`{"device_id":"NOT-A-REAL-DEVICE"}`))
	rec := httptest.NewRecorder()
	s.handleAcceptPair(rec, req)

	if !called {
		t.Fatal("the request never reached the daemon, which is what decides")
	}
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("got %d, want 422", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "waiting for approval") {
		t.Errorf("unhelpful error body: %q", rec.Body.String())
	}
}

// A missing ID is refused before the daemon is troubled at all.
func TestAcceptPairRejectsAnEmptyID(t *testing.T) {
	s := &Server{}
	s.actions.AcceptPair = func(deviceID string) error {
		t.Fatal("an empty device id reached the daemon")
		return nil
	}
	for _, body := range []string{`{}`, `{"device_id":"  "}`} {
		req := httptest.NewRequest(http.MethodPost, "/api/pair/accept", strings.NewReader(body))
		rec := httptest.NewRecorder()
		s.handleAcceptPair(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("body %s: got %d, want 400", body, rec.Code)
		}
	}
}

// Undoing changes made to a received backup: the folder id goes to the daemon,
// which is the only thing that knows which folders this machine actually
// receives — and therefore which ones may be reverted at all.
func TestRevertFolderPassesTheFolderIDThrough(t *testing.T) {
	s := &Server{}
	got := ""
	s.actions.RevertFolder = func(folderID string) error {
		got = folderID
		return nil
	}
	req := httptest.NewRequest(http.MethodPost, "/api/receive/revert",
		strings.NewReader(`{"folder_id":"kqz3d-8xh2p"}`))
	rec := httptest.NewRecorder()
	s.handleRevertFolder(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("got %d: %s", rec.Code, rec.Body.String())
	}
	if got != "kqz3d-8xh2p" {
		t.Errorf("folder id = %q, want the one from the body", got)
	}
}

// A folder this machine does not receive — its own data, or an id from a stale
// tab — must be refused by the daemon and reported as a 422. Reverting the
// wrong folder would replace the user's files with a remote copy.
func TestRevertFolderRejectsAFolderThatIsNotReceivedHere(t *testing.T) {
	s := &Server{}
	called := false
	s.actions.RevertFolder = func(folderID string) error {
		called = true
		return errors.New(`"mine" is not a backup this machine receives`)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/receive/revert", strings.NewReader(`{"folder_id":"mine"}`))
	rec := httptest.NewRecorder()
	s.handleRevertFolder(rec, req)

	if !called {
		t.Fatal("the request never reached the daemon, which is what decides")
	}
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("got %d, want 422", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "not a backup this machine receives") {
		t.Errorf("unhelpful error body: %q", rec.Body.String())
	}
}

// A missing id is refused before the daemon is troubled at all.
func TestRevertFolderRejectsAnEmptyID(t *testing.T) {
	s := &Server{}
	s.actions.RevertFolder = func(folderID string) error {
		t.Fatal("an empty folder id reached the daemon")
		return nil
	}
	for _, body := range []string{`{}`, `{"folder_id":"  "}`} {
		req := httptest.NewRequest(http.MethodPost, "/api/receive/revert", strings.NewReader(body))
		rec := httptest.NewRecorder()
		s.handleRevertFolder(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("body %s: got %d, want 400", body, rec.Code)
		}
	}
}

// Deleting files that exist only on this computer stays behind the same token
// as everything else on the loopback dashboard.
func TestRevertFolderNeedsAuthentication(t *testing.T) {
	s := &Server{state: &config.State{IPCToken: "the-token"}}
	s.actions.RevertFolder = func(string) error {
		t.Fatal("an unauthenticated request reached the handler")
		return nil
	}
	h := s.requireToken(s.handleRevertFolder)

	rec := httptest.NewRecorder()
	h(rec, httptest.NewRequest(http.MethodPost, "/api/receive/revert", strings.NewReader(`{"folder_id":"f1"}`)))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("got %d, want 401", rec.Code)
	}

	reached := false
	s.actions.RevertFolder = func(string) error { reached = true; return nil }
	req := httptest.NewRequest(http.MethodPost, "/api/receive/revert", strings.NewReader(`{"folder_id":"f1"}`))
	req.Header.Set("Authorization", "Bearer the-token")
	rec = httptest.NewRecorder()
	h(rec, req)
	if rec.Code != http.StatusOK || !reached {
		t.Errorf("authenticated request got %d (reached=%v), want 200", rec.Code, reached)
	}
}

const testDeviceID = "ABCDEFG-HIJKLMN-OPQRSTU-VWXYZ01-2345678-9ABCDEF-GHIJKLM-NOPQRST"

// Nobody should have to transcribe 63 characters. The response has to be a real
// PNG, because the page hands it straight to an <img>.
func TestPairQRRendersThisMachinesID(t *testing.T) {
	s := &Server{}
	asked := false
	s.actions.DeviceID = func() (string, error) {
		asked = true
		return testDeviceID, nil
	}
	req := httptest.NewRequest(http.MethodGet, "/api/pair/qr", nil)
	rec := httptest.NewRecorder()
	s.handlePairQR(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("got %d: %s", rec.Code, rec.Body.String())
	}
	if !asked {
		t.Error("the QR was drawn without asking the daemon for this machine's ID")
	}
	if ct := rec.Header().Get("Content-Type"); ct != "image/png" {
		t.Errorf("content type = %q, want image/png", ct)
	}
	body := rec.Body.Bytes()
	if !bytes.HasPrefix(body, []byte("\x89PNG\r\n\x1a\n")) {
		t.Errorf("body is not a PNG: % x", body[:min(8, len(body))])
	}
	if len(body) < 500 {
		t.Errorf("PNG is only %d bytes; that cannot be a scannable code", len(body))
	}

	// It must encode the ID itself: a QR of anything else is unusable by the
	// person holding the phone.
	img, err := png.Decode(bytes.NewReader(body))
	if err != nil {
		t.Fatalf("decoding the PNG: %v", err)
	}
	want, err := qrcode.Encode(testDeviceID, qrcode.Medium, qrPixels)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(body, want) {
		t.Error("the QR does not encode the bare device ID")
	}
	if b := img.Bounds(); b.Dx() < 200 || b.Dy() < 200 {
		t.Errorf("image is %dx%d; too small to scan comfortably", b.Dx(), b.Dy())
	}
}

// A machine with no machine sync configured has no identity yet. That is a
// normal state, not a failure, and it must not render a broken image.
func TestPairQRWithoutADeviceID(t *testing.T) {
	for _, c := range []struct {
		name string
		fn   func() (string, error)
	}{
		{"engine not running", func() (string, error) { return "", errors.New("engine isn't running yet") }},
		{"empty id", func() (string, error) { return "", nil }},
	} {
		t.Run(c.name, func(t *testing.T) {
			s := &Server{}
			s.actions.DeviceID = c.fn
			rec := httptest.NewRecorder()
			s.handlePairQR(rec, httptest.NewRequest(http.MethodGet, "/api/pair/qr", nil))
			if rec.Code != http.StatusServiceUnavailable {
				t.Errorf("got %d, want 503", rec.Code)
			}
		})
	}
}

// The QR is this machine's identity: it stays behind the same token as
// everything else on the loopback dashboard.
func TestPairQRNeedsAuthentication(t *testing.T) {
	s := &Server{state: &config.State{IPCToken: "the-token"}}
	s.actions.DeviceID = func() (string, error) {
		t.Fatal("an unauthenticated request reached the handler")
		return "", nil
	}
	h := s.requireToken(s.handlePairQR)

	rec := httptest.NewRecorder()
	h(rec, httptest.NewRequest(http.MethodGet, "/api/pair/qr", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("got %d, want 401", rec.Code)
	}

	// And with the token it is served.
	s.actions.DeviceID = func() (string, error) { return testDeviceID, nil }
	req := httptest.NewRequest(http.MethodGet, "/api/pair/qr", nil)
	req.Header.Set("Authorization", "Bearer the-token")
	rec = httptest.NewRecorder()
	h(rec, req)
	if rec.Code != http.StatusOK {
		t.Errorf("authenticated request got %d, want 200", rec.Code)
	}
}

// dashboard is a real server — same mux, same middleware chain the daemon
// builds — on an ephemeral port, so the origin guard is exercised where it
// actually sits rather than in isolation. Every wired action just counts calls,
// so a test can prove a refused request never reached the daemon.
type dashboard struct {
	*Server
	base  string
	calls int
}

func newDashboard(t *testing.T) *dashboard {
	t.Helper()
	d := &dashboard{}
	touch := func() { d.calls++ }

	cfg := &config.Config{}
	cfg.General.DashboardPort = 0 // ephemeral: never collide with a running daemon
	state := &config.State{IPCToken: "the-token"}
	s, err := New(cfg, state, slog.New(slog.DiscardHandler), func() any { return map[string]any{} }, Actions{
		AcceptPair:       func(string) error { touch(); return nil },
		RevertFolder:     func(string) error { touch(); return nil },
		Wake:             func(string) error { touch(); return nil },
		AddShare:         func(AddShareRequest) error { touch(); return nil },
		CompleteSetup:    func() error { touch(); return nil },
		RemoveFolder:     func(string) error { touch(); return nil },
		RemoveTarget:     func(string) error { touch(); return nil },
		SetFolderIgnores: func(string, []string, bool) error { touch(); return nil },
		CreateBackup:     func(BackupRequest) (any, error) { touch(); return map[string]bool{"ok": true}, nil },
		AddArchive:       func(ArchiveRequest) error { touch(); return nil },
	})
	if err != nil {
		t.Fatalf("starting the dashboard: %v", err)
	}
	t.Cleanup(func() { _ = s.ln.Close() })

	d.Server = s
	d.base = fmt.Sprintf("http://127.0.0.1:%d", s.ln.Addr().(*net.TCPAddr).Port)
	return d
}

// do drives the fully composed handler (loopbackOnly → fromTheDashboardOnly →
// mux), addressed at the dashboard's own URL so the Host header is realistic.
func (d *dashboard) do(method, path, body string, headers map[string]string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, d.base+path, strings.NewReader(body))
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	d.http.Handler.ServeHTTP(rec, req)
	return rec
}

// fromThePage is what the dashboard's own JS sends: the session cookie, the
// custom header mutate() adds, and the labels the browser attaches by itself.
// origin is the origin the page was loaded from, which is NOT always the port
// the daemon bound — see TestTunnelledDashboardCanStillMutate.
func fromThePage(origin string) map[string]string {
	return map[string]string{
		"Origin":         origin,
		"Sec-Fetch-Site": "same-origin",
		"Cookie":         sessionCookie + "=the-token",
		MutationHeader:   "1",
	}
}

// Every route that changes something. The guarantee is repo-wide, not
// per-route: a route added later inherits it from the middleware.
var mutatingRoutes = []struct{ method, path, body string }{
	{http.MethodPost, "/api/pair/accept", `{"device_id":"` + testDeviceID + `"}`},
	{http.MethodPost, "/api/receive/revert", `{"folder_id":"kqz3d-8xh2p"}`},
	{http.MethodPost, "/api/wake", `{"target":"nas"}`},
	{http.MethodPost, "/api/backups", `{"path":"/tmp/x"}`},
	{http.MethodPost, "/api/targets/share", `{"url":"//nas/backups","name":"nas"}`},
	{http.MethodPost, "/api/archives", `{"name":"weekly","every":"7d","target":"nas","password":"x"}`},
	{http.MethodPost, "/api/setup/complete", ``},
	{http.MethodPost, "/api/folders/abc/ignores", `{"ignores":[]}`},
	{http.MethodDelete, "/api/folders/abc", ``},
	{http.MethodDelete, "/api/targets/nas", ``},
}

// A page on ANY other loopback port must not be able to change anything here,
// even though the browser hands it a fully valid session.
//
// Browsers have no registrable domain for a bare IP, so every port on 127.0.0.1
// is the same SITE: the session cookie DOES ride along on this request (the
// browser labels it Sec-Fetch-Site: same-site, so SameSite=Strict does not hold
// it back) and the Host header is the dashboard's own, so loopbackOnly passes it
// too. Without this guard any other local process — or any other user on a
// shared machine — could approve a pairing request and gain a standing right to
// write here, with no token and no click.
//
// What refuses it is the header the page cannot send (that would need a CORS
// preflight, which this server never answers) and the browser's own
// Sec-Fetch-Site label. NOT the port in Origin: a legitimate ssh tunnel produces
// a byte-identical Origin — see TestTunnelledDashboardCanStillMutate.
func TestMutatingRoutesRejectAnotherLoopbackPort(t *testing.T) {
	const attacker = "http://127.0.0.1:28999"
	attacks := []struct {
		name    string
		headers func(d *dashboard) map[string]string
	}{
		// Exactly what a browser sends for a credentialed fetch() from a page
		// on another loopback port. No custom header: the page cannot set one.
		{"as a browser really sends it", func(*dashboard) map[string]string {
			return map[string]string{
				"Origin":         attacker,
				"Sec-Fetch-Site": "same-site",
				"Cookie":         sessionCookie + "=the-token",
			}
		}},
		// The same, riding a leaked bearer token instead of the cookie.
		{"with a bearer token", func(*dashboard) map[string]string {
			return map[string]string{
				"Origin":         attacker,
				"Sec-Fetch-Site": "same-site",
				"Authorization":  "Bearer the-token",
			}
		}},
		// Suppose the page could somehow set the header: Sec-Fetch-Site still
		// names it, and the BROWSER sets that one, not the page.
		{"with the header forged", func(*dashboard) map[string]string {
			return map[string]string{
				"Origin":         attacker,
				"Sec-Fetch-Site": "same-site",
				"Cookie":         sessionCookie + "=the-token",
				MutationHeader:   "1",
			}
		}},
		// And suppose it could forge Origin as well — no browser lets a page do
		// this — the missing header is still fatal.
		{"with the origin forged", func(d *dashboard) map[string]string {
			return map[string]string{
				"Origin": d.base,
				"Cookie": sessionCookie + "=the-token",
			}
		}},
	}
	for _, c := range mutatingRoutes {
		for _, a := range attacks {
			d := newDashboard(t)
			rec := d.do(c.method, c.path, c.body, a.headers(d))
			if rec.Code != http.StatusForbidden {
				t.Errorf("%s %s %s: got %d, want 403", c.method, c.path, a.name, rec.Code)
			}
			if d.calls != 0 {
				t.Errorf("%s %s %s REACHED the daemon", c.method, c.path, a.name)
			}
		}
	}
}

// Sec-Fetch-Site is browser-set and port-agnostic, so it stands on its own:
// anything that is not same-origin (or none, for a typed URL) is refused even
// when everything else about the request looks right.
func TestMutatingRoutesRejectSameSiteFetches(t *testing.T) {
	for _, site := range []string{"same-site", "cross-site"} {
		for _, c := range mutatingRoutes {
			d := newDashboard(t)
			headers := fromThePage(d.base)
			headers["Sec-Fetch-Site"] = site
			rec := d.do(c.method, c.path, c.body, headers)
			if rec.Code != http.StatusForbidden {
				t.Errorf("%s %s with Sec-Fetch-Site: %s got %d, want 403", c.method, c.path, site, rec.Code)
			}
			if d.calls != 0 {
				t.Errorf("%s %s with Sec-Fetch-Site: %s REACHED the daemon", c.method, c.path, site)
			}
		}
	}
}

// The custom header is the primary defence, so its absence must be fatal on its
// own — with an otherwise perfect request from the dashboard's own origin.
// (A call left on plain fetch() in the page fails here too, loudly.)
func TestMutatingRoutesRequireTheDashboardsHeader(t *testing.T) {
	for _, c := range mutatingRoutes {
		d := newDashboard(t)
		headers := fromThePage(d.base)
		delete(headers, MutationHeader)
		rec := d.do(c.method, c.path, c.body, headers)
		if rec.Code != http.StatusForbidden {
			t.Errorf("%s %s without %s: got %d, want 403", c.method, c.path, MutationHeader, rec.Code)
		}
		if d.calls != 0 {
			t.Errorf("%s %s without %s REACHED the daemon", c.method, c.path, MutationHeader)
		}
	}
}

// Reaching a headless machine's dashboard over `ssh -L 18666:127.0.0.1:8666` is
// THE documented way to administer it, and the tunnel's local port MUST differ
// from the remote one whenever the admin also runs backup-maker locally. The
// browser then loads http://127.0.0.1:18666/ and labels every request from it
// Origin: http://127.0.0.1:18666 — a different port from the one the daemon
// bound, and byte-identical to what an attacking page on another port sends.
//
// So the port can never be the test: demanding equality only breaks the tunnel,
// which is how this was caught (every button on a tunnelled dashboard, Approve
// included, silently 403'd). Page and API still share one origin through the
// tunnel, so the page can set the header and the browser says same-origin —
// which is what actually separates the two cases.
func TestTunnelledDashboardCanStillMutate(t *testing.T) {
	for _, origin := range []string{
		"http://127.0.0.1:18666", // ssh -L 18666:127.0.0.1:8666
		"http://localhost:18666", // the same tunnel, typed by name
		"http://[::1]:18666",     // ssh -L binding localhost's v6 address
		"http://127.0.0.1:8666",  // the ordinary local dashboard
	} {
		for _, c := range mutatingRoutes {
			d := newDashboard(t)
			rec := d.do(c.method, c.path, c.body, fromThePage(origin))
			if rec.Code != http.StatusOK {
				t.Errorf("%s %s from a dashboard at %s: got %d (%s), want 200",
					c.method, c.path, origin, rec.Code, rec.Body.String())
			}
			if d.calls == 0 {
				t.Errorf("%s %s from a dashboard at %s never reached the daemon", c.method, c.path, origin)
			}
		}
	}

	// And from this server's own address, whatever port it actually bound.
	for _, c := range mutatingRoutes {
		d := newDashboard(t)
		rec := d.do(c.method, c.path, c.body, fromThePage(d.base))
		if rec.Code != http.StatusOK {
			t.Errorf("%s %s from the dashboard's own origin: got %d (%s), want 200",
				c.method, c.path, rec.Code, rec.Body.String())
		}
		if d.calls == 0 {
			t.Errorf("%s %s from the dashboard's own origin never reached the daemon", c.method, c.path)
		}
	}
}

// A page on a real website is refused whatever else it manages to send. (It
// could not reach the listener anyway, but the rule should not depend on that.)
func TestMutatingRoutesRejectRemoteOrigins(t *testing.T) {
	for _, origin := range []string{"http://evil.example.com", "https://evil.example.com", "null"} {
		d := newDashboard(t)
		headers := fromThePage(origin)
		delete(headers, "Sec-Fetch-Site") // give it every benefit of the doubt
		rec := d.do(http.MethodPost, "/api/pair/accept", `{"device_id":"`+testDeviceID+`"}`, headers)
		if rec.Code != http.StatusForbidden {
			t.Errorf("origin %s: got %d, want 403", origin, rec.Code)
		}
		if d.calls != 0 {
			t.Errorf("origin %s REACHED the daemon", origin)
		}
	}
}

// The header defence rests entirely on this: a page on another origin can only
// send a custom header if the browser's preflight is answered with permission.
// This server must never answer one — no Access-Control-Allow-* on any route,
// ever — or the cross-port page could set the header and walk straight in.
func TestNoCORSPreflightIsEverAnswered(t *testing.T) {
	for _, c := range mutatingRoutes {
		d := newDashboard(t)
		rec := d.do(http.MethodOptions, c.path, "", map[string]string{
			"Origin":                         "http://127.0.0.1:28999",
			"Access-Control-Request-Method":  c.method,
			"Access-Control-Request-Headers": MutationHeader,
		})
		for h := range rec.Header() {
			if strings.HasPrefix(strings.ToLower(h), "access-control-") {
				t.Errorf("OPTIONS %s answered with %s: that would hand the header to any local page", c.path, h)
			}
		}
		if rec.Code == http.StatusOK || rec.Code == http.StatusNoContent {
			t.Errorf("OPTIONS %s returned %d; a preflight must not succeed", c.path, rec.Code)
		}
		if d.calls != 0 {
			t.Errorf("OPTIONS %s REACHED the daemon", c.path)
		}
	}
}

// curl, the CLI and the tests send no Origin and no Sec-Fetch-Site. A browser
// cannot suppress them, so their absence is not something an attacking page can
// use — and requiring them would break every command-line recipe. The header
// they DO have to send is one line in the CLI's HTTP client.
func TestMutatingRoutesAcceptRequestsWithoutAnOrigin(t *testing.T) {
	for _, c := range mutatingRoutes {
		d := newDashboard(t)
		rec := d.do(c.method, c.path, c.body, map[string]string{
			"Authorization": "Bearer the-token",
			MutationHeader:  "1",
		})
		if rec.Code != http.StatusOK {
			t.Errorf("%s %s from a token-bearing client: got %d (%s), want 200", c.method, c.path, rec.Code, rec.Body.String())
		}
		if d.calls == 0 {
			t.Errorf("%s %s never reached the daemon", c.method, c.path)
		}
	}
}

// The guard is a second gate, not a replacement for the first: a request that
// looks exactly like the dashboard's own but carries no credentials is still
// nobody.
func TestOriginCheckDoesNotReplaceAuthentication(t *testing.T) {
	for _, c := range mutatingRoutes {
		d := newDashboard(t)
		headers := fromThePage(d.base)
		delete(headers, "Cookie")
		rec := d.do(c.method, c.path, c.body, headers)
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("%s %s with no token: got %d, want 401", c.method, c.path, rec.Code)
		}
		if d.calls != 0 {
			t.Errorf("%s %s with no token REACHED the daemon", c.method, c.path)
		}
	}
}

// /auth is a GET, and a user pastes or clicks it: a browser reports that as
// Sec-Fetch-Site: none (typed) or cross-site (clicked from a terminal or mail
// client), with no Origin at all. Reads are exempt from the origin guard for
// exactly this reason — logging in must not depend on where the link was
// clicked. It stays safe because it proves possession of the token.
func TestAuthLinkStillWorksFromOutsideThePage(t *testing.T) {
	for _, site := range []string{"none", "cross-site", ""} {
		d := newDashboard(t)
		headers := map[string]string{}
		if site != "" {
			headers["Sec-Fetch-Site"] = site
		}
		rec := d.do(http.MethodGet, "/auth?token=the-token", "", headers)
		if rec.Code != http.StatusSeeOther {
			t.Errorf("Sec-Fetch-Site %q: /auth got %d, want a redirect", site, rec.Code)
		}
		if !strings.Contains(rec.Header().Get("Set-Cookie"), sessionCookie) {
			t.Errorf("Sec-Fetch-Site %q: /auth set no session cookie", site)
		}
	}
	// And a wrong token is still refused, whatever it claims about itself.
	d := newDashboard(t)
	if rec := d.do(http.MethodGet, "/auth?token=wrong", "", nil); rec.Code != http.StatusForbidden {
		t.Errorf("/auth with a bad token got %d, want 403", rec.Code)
	}
}

// The session must outlive the browser, or a plain http://127.0.0.1:<port>
// bookmark bounces after every restart and people bookmark the ?token=... URL
// instead — which puts a live credential in a syncable bookmark.
func TestTheSessionSurvivesClosingTheBrowser(t *testing.T) {
	d := newDashboard(t)
	rec := d.do(http.MethodGet, "/auth?token=the-token", "", nil)

	var session *http.Cookie
	for _, c := range (&http.Response{Header: rec.Header()}).Cookies() {
		if c.Name == sessionCookie {
			session = c
		}
	}
	if session == nil {
		t.Fatal("/auth set no session cookie")
	}
	if session.MaxAge <= 0 {
		t.Fatalf("session cookie has MaxAge %d — it dies with the browser, so a bookmark cannot work", session.MaxAge)
	}
	if got := time.Duration(session.MaxAge) * time.Second; got < 180*24*time.Hour {
		t.Errorf("session lasts %s; a bookmark has to survive a long gap, or it silently stops working", got)
	}
	// The properties that make a persistent credential acceptable at all.
	if !session.HttpOnly {
		t.Error("session cookie must stay HttpOnly — page scripts must never read the token")
	}
	if session.SameSite != http.SameSiteStrictMode {
		t.Error("session cookie must stay SameSite=Strict")
	}
}

// A dashboard someone actually uses must never quietly log itself out. Every
// authenticated request slides the expiry forward, so the 30 days is really
// "how long you can ignore it", not "how long until you visit the terminal".
func TestUsingTheDashboardKeepsItSignedIn(t *testing.T) {
	d := newDashboard(t)
	rec := d.do(http.MethodGet, "/api/status", "", map[string]string{
		"Cookie": sessionCookie + "=the-token",
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("cookie-authenticated GET got %d, want 200", rec.Code)
	}
	var renewed *http.Cookie
	for _, c := range (&http.Response{Header: rec.Header()}).Cookies() {
		if c.Name == sessionCookie {
			renewed = c
		}
	}
	if renewed == nil {
		t.Fatal("an authenticated request did not renew the session; it will expire mid-use")
	}
	if renewed.MaxAge <= 0 {
		t.Errorf("renewed cookie has MaxAge %d, want the full lifetime", renewed.MaxAge)
	}
}

// Reads on the loopback dashboard are untouched by the origin guard: they change
// nothing, and the page polls them from itself.
func TestReadsAreNotBlockedByTheOriginGuard(t *testing.T) {
	d := newDashboard(t)
	for _, path := range []string{"/api/ping", "/api/status"} {
		rec := d.do(http.MethodGet, path, "", map[string]string{
			"Origin":        "http://127.0.0.1:28999",
			"Authorization": "Bearer the-token",
		})
		if rec.Code != http.StatusOK {
			t.Errorf("GET %s got %d, want 200", path, rec.Code)
		}
	}
}

// The network view is a different listener with a different wrapper, so the
// origin guard is not in its path — its allowed GETs must still be served, and
// its refusals must still come from the allow-list.
func TestLANViewIsUnaffectedByTheOriginGuard(t *testing.T) {
	d := newDashboard(t)
	h := lanReadOnly(d.mux, d.status, nil) // exactly what StartLANView wraps

	for _, path := range []string{"/api/ping", "/api/status", "/"} {
		req := httptest.NewRequest(http.MethodGet, "http://192.168.1.10:8667"+path, nil)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Errorf("GET %s on the network view got %d, want 200", path, rec.Code)
		}
	}

	req := httptest.NewRequest(http.MethodPost, "http://192.168.1.10:8667/api/pair/accept", strings.NewReader("{}"))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "read-only") {
		t.Errorf("the network view's refusal changed: %d %q", rec.Code, rec.Body.String())
	}
	if d.calls != 0 {
		t.Error("a network-view POST reached the daemon")
	}
}

// Every action is optional at wiring time; a missing one must degrade to an
// honest error rather than panicking and taking the daemon down.
func TestHandlersWithoutActionsDoNotPanic(t *testing.T) {
	s := &Server{}
	cases := []struct {
		name    string
		handler http.HandlerFunc
		method  string
		body    string
	}{
		{"machines", s.handleMachines, http.MethodGet, ""},
		{"storage", s.handleStorage, http.MethodPost, "{}"},
		{"create backup", s.handleCreateBackup, http.MethodPost, "{}"},
		{"remove folder", s.handleRemoveFolder, http.MethodDelete, ""},
		{"set folder excludes", s.handleSetFolderIgnores, http.MethodPost, "{}"},
		{"remove target", s.handleRemoveTarget, http.MethodDelete, ""},
		{"add archive", s.handleAddArchive, http.MethodPost, "{}"},
		{"accept pair", s.handleAcceptPair, http.MethodPost, `{"device_id":"SOMETHING"}`},
		{"revert received folder", s.handleRevertFolder, http.MethodPost, `{"folder_id":"SOMETHING"}`},
		{"pairing QR", s.handlePairQR, http.MethodGet, ""},
		{"complete setup", s.handleCompleteSetup, http.MethodPost, ""},
	}
	for _, c := range cases {
		var body *strings.Reader
		if c.body != "" {
			body = strings.NewReader(c.body)
		} else {
			body = strings.NewReader("")
		}
		req := httptest.NewRequest(c.method, "/x", body)
		rec := httptest.NewRecorder()
		c.handler(rec, req)
		if rec.Code != http.StatusServiceUnavailable {
			t.Errorf("%s: got %d, want 503 when the action is unwired", c.name, rec.Code)
		}
	}
}
