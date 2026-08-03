// SPDX-License-Identifier: MIT

package webui

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// deviceBook is a stand-in for the daemon's device list.
type deviceBook struct {
	mu       sync.Mutex
	approved map[string]bool // token -> approved
	codes    map[string]string
	names    map[string]string
	next     int
	// forget makes a token stop matching, standing in for a request that
	// lapsed while somebody was typing a name into it.
	forget map[string]bool
}

func newBook() *deviceBook {
	return &deviceBook{
		approved: map[string]bool{}, codes: map[string]string{},
		names: map[string]string{}, forget: map[string]bool{},
	}
}

func (b *deviceBook) seen(token, addr, agent string) (bool, string, string, string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if code, ok := b.codes[token]; ok && token != "" {
		return b.approved[token], code, b.names[token], ""
	}
	b.next++
	issued := "token-" + string(rune('a'+b.next-1))
	code := "CODE-" + string(rune('A'+b.next-1))
	b.codes[issued] = code
	return false, code, "", issued
}

func (b *deviceBook) named(token, name string) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	if _, ok := b.codes[token]; !ok || b.forget[token] {
		return false
	}
	b.names[token] = name
	return true
}

func (b *deviceBook) nameOf(token string) string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.names[token]
}

func (b *deviceBook) approve(token string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.approved[token] = true
}

func gated(book *deviceBook, armed bool) http.Handler {
	reached := false
	return lanReadOnly(everythingHandler(&reached),
		func() any { return map[string]any{"machine_name": "workstation"} },
		&LANGate{
			ApprovedOnly: func() bool { return armed },
			Seen:         book.seen,
			Named:        book.named,
		})
}

// THE POINT OF THE WHOLE FEATURE: a device that has not been approved learns
// nothing about this machine's backups. Not the folder labels, not whether
// anything is failing — nothing but the fact that it must be approved.
func TestAnUnapprovedDeviceIsToldNothingAboutTheBackups(t *testing.T) {
	h := gated(newBook(), true)

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/status", nil))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status returned %d to an unapproved device, want 403", rec.Code)
	}
	if strings.Contains(rec.Body.String(), "workstation") {
		t.Errorf("the machine name leaked to an unapproved device: %s", rec.Body.String())
	}

	// And the page it gets is the holding page, not the dashboard shell —
	// which would otherwise tell it what this machine runs.
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if rec.Code != http.StatusForbidden {
		t.Errorf("the page returned %d, want 403", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "waiting for approval") {
		t.Errorf("an unapproved device was not told what to do: %s", body)
	}
	// Any code will do — a cookie-less request is a new device every time, so
	// this is the SECOND one. What matters is that a code is shown at all.
	if !strings.Contains(body, "CODE-") {
		t.Errorf("the holding page showed no code, so there is no way to tell which device to approve: %s", body)
	}
	// A token has to be issued, or the device is a stranger again on every
	// request and the pending list fills with duplicates of one phone.
	if !strings.Contains(rec.Header().Get("Set-Cookie"), lanDeviceCookie) {
		t.Error("no device token was issued")
	}
}

// Approval admits exactly that device — and, once revoked, stops.
func TestApprovingOneDeviceAdmitsOnlyThatDevice(t *testing.T) {
	book := newBook()
	h := gated(book, true)

	// First contact issues token-a.
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	book.approve("token-a")

	withToken := httptest.NewRequest(http.MethodGet, "/api/status", nil)
	withToken.AddCookie(&http.Cookie{Name: lanDeviceCookie, Value: "token-a"})
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, withToken)
	if rec.Code != http.StatusOK {
		t.Fatalf("an approved device got %d, want 200", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "workstation") {
		t.Errorf("an approved device was not served the status: %s", rec.Body.String())
	}

	// Somebody else's browser, or the same one with a guessed token, is not
	// admitted by association.
	other := httptest.NewRequest(http.MethodGet, "/api/status", nil)
	other.AddCookie(&http.Cookie{Name: lanDeviceCookie, Value: "token-guessed"})
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, other)
	if rec.Code == http.StatusOK {
		t.Error("an unknown token was admitted: approval is supposed to name one device")
	}
}

// With the gate disarmed the view behaves exactly as it did before this
// existed. An upgrade must not lock out a phone that worked yesterday.
func TestWithAccessOpenEveryDeviceStillReadsTheView(t *testing.T) {
	h := gated(newBook(), false)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/status", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("open mode returned %d, want 200", rec.Code)
	}
	if sc := rec.Header().Get("Set-Cookie"); sc != "" {
		t.Errorf("open mode issued a device token it has no use for: %s", sc)
	}
}

// The gate must not become a way in. An approved device is still read-only.
func TestAnApprovedDeviceStillCannotChangeAnything(t *testing.T) {
	book := newBook()
	h := gated(book, true)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	book.approve("token-a")

	for _, path := range []string{"/api/settings", "/api/lan-devices/CODE-A/approve", "/api/backups"} {
		req := httptest.NewRequest(http.MethodPost, path, strings.NewReader("{}"))
		req.AddCookie(&http.Cookie{Name: lanDeviceCookie, Value: "token-a"})
		rec = httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusForbidden {
			t.Errorf("POST %s from an approved device returned %d, want 403", path, rec.Code)
		}
	}
}
