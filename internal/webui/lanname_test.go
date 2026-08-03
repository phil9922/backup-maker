// SPDX-License-Identifier: MIT

package webui

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// nameRequest is the POST the holding page makes when somebody types a name.
func nameRequest(token, name string) *http.Request {
	r := httptest.NewRequest(http.MethodPost, "/name",
		strings.NewReader("name="+strings.ReplaceAll(name, " ", "+")))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if token != "" {
		r.AddCookie(&http.Cookie{Name: lanDeviceCookie, Value: token})
	}
	return r
}

// The holding page has to ask, or there is no name to show. It also has to show
// back what was given, so the person who typed it can see that it took.
func TestTheHoldingPageAsksTheDeviceWhatItIs(t *testing.T) {
	book := newBook()
	h := gated(book, true)

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	body := rec.Body.String()
	if !strings.Contains(body, `action="/name"`) {
		t.Fatalf("the holding page does not ask the device to name itself: %s", body)
	}

	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, nameRequest("token-a", "Alex's phone"))
	if rec.Code != http.StatusNoContent {
		t.Fatalf("naming returned %d: %s", rec.Code, rec.Body.String())
	}
	if got := book.nameOf("token-a"); got != "Alex's phone" {
		t.Fatalf("the name did not reach the device list: %q", got)
	}

	withToken := httptest.NewRequest(http.MethodGet, "/", nil)
	withToken.AddCookie(&http.Cookie{Name: lanDeviceCookie, Value: "token-a"})
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, withToken)
	if !strings.Contains(rec.Body.String(), "Alex&#39;s phone") {
		t.Errorf("the holding page did not show the name back: %s", rec.Body.String())
	}
}

// THE NAME IS THE ONE ATTACKER-CHOSEN STRING ON THAT PAGE, and the page is
// assembled by concatenation. Escaped, it is text; unescaped, an unapproved
// device on the wifi writes script into a page this program serves.
func TestANameCannotPutMarkupIntoTheHoldingPage(t *testing.T) {
	book := newBook()
	h := gated(book, true)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))

	const attack = `"><script>alert(1)</script>`
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, nameRequest("token-a", attack))
	if rec.Code != http.StatusNoContent {
		t.Fatalf("naming returned %d", rec.Code)
	}

	withToken := httptest.NewRequest(http.MethodGet, "/", nil)
	withToken.AddCookie(&http.Cookie{Name: lanDeviceCookie, Value: "token-a"})
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, withToken)
	if strings.Contains(rec.Body.String(), "<script>alert(1)</script>") {
		t.Errorf("a device wrote markup into the holding page: %s", rec.Body.String())
	}
}

// A name is a label. Nothing on this endpoint may scale with what the client
// sends — it is reachable by anything on the network, unauthenticated.
func TestNamingRefusesAnAbsurdBody(t *testing.T) {
	book := newBook()
	h := gated(book, true)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))

	huge := httptest.NewRequest(http.MethodPost, "/name",
		strings.NewReader("name="+strings.Repeat("a", 1<<20)))
	huge.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	huge.AddCookie(&http.Cookie{Name: lanDeviceCookie, Value: "token-a"})
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, huge)
	if rec.Code == http.StatusNoContent {
		t.Error("a megabyte was accepted as a device name")
	}
	if got := book.nameOf("token-a"); got != "" {
		t.Errorf("part of an oversized body was stored: %d characters", len(got))
	}
}

// The route belongs to the holding page, not to the view. An approved device
// has no business posting a name, and neither does anyone when the gate is off
// — there are no pending devices to name in either case.
func TestNamingIsNotAvailableToAnApprovedDeviceOrWithTheGateOff(t *testing.T) {
	book := newBook()
	h := gated(book, true)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	book.approve("token-a")

	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, nameRequest("token-a", "something else"))
	if rec.Code == http.StatusNoContent {
		t.Error("an approved device reached the naming route")
	}

	open := gated(newBook(), false)
	rec = httptest.NewRecorder()
	open.ServeHTTP(rec, nameRequest("token-a", "anything"))
	if rec.Code == http.StatusNoContent {
		t.Error("the naming route exists when the gate is switched off")
	}
}

// A request can lapse while somebody is still typing into it — five minutes is
// not long if the phone went into a pocket. The name must not be lost to that:
// the same POST is filed as a fresh request, the name lands on THAT, and the
// browser leaves holding the token for it. Anything else would be a person
// typing a name, seeing it accepted, and watching an anonymous row appear on
// the dashboard.
func TestANameSurvivesTheRequestLapsingUnderneathIt(t *testing.T) {
	book := newBook()
	h := gated(book, true)

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, nameRequest("token-that-lapsed", "Alex's phone"))
	if rec.Code != http.StatusNoContent {
		t.Fatalf("naming after a lapse returned %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Header().Get("Set-Cookie"), lanDeviceCookie) {
		t.Fatal("no token was issued, so the browser is a stranger again and the name is orphaned")
	}
	// token-a is what the stand-in issues first, and the name must be on it —
	// not on the dead token the browser arrived with.
	if got := book.nameOf("token-a"); got != "Alex's phone" {
		t.Errorf("the name landed nowhere useful: %q", got)
	}
	if got := book.nameOf("token-that-lapsed"); got != "" {
		t.Error("a name was written against a token that matches no record")
	}
}

// Naming yourself is not a way in. It writes one bounded string into your own
// record and changes nothing about what you may read.
func TestNamingADeviceDoesNotAdmitIt(t *testing.T) {
	book := newBook()
	h := gated(book, true)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))

	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, nameRequest("token-a", "the household NAS"))

	after := httptest.NewRequest(http.MethodGet, "/api/status", nil)
	after.AddCookie(&http.Cookie{Name: lanDeviceCookie, Value: "token-a"})
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, after)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("a device that named itself got %d from the status API, want 403", rec.Code)
	}
	if strings.Contains(rec.Body.String(), "workstation") {
		t.Error("a device read the status after naming itself")
	}
}

// The names devices give themselves are typed by whoever is on the wifi, and
// the network view is served to whoever is on the wifi. Republishing them there
// would turn this into a message board that anybody can post to and everybody
// on the network reads.
func TestDeviceNamesAreNotPublishedOnTheNetworkView(t *testing.T) {
	redacted, ok := RedactForNetwork(map[string]any{
		"machine_name": "my-laptop",
		"settings": map[string]any{
			"lan_devices": []any{map[string]any{"code": "ABC-123", "name": "Alex's phone"}},
		},
	}).(map[string]any)
	if !ok {
		t.Fatal("redaction did not return an object")
	}
	if _, still := redacted["settings"]; still {
		t.Error("the device list, and the names on it, reached the network view")
	}
}
