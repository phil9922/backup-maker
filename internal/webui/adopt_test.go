// SPDX-License-Identifier: MIT

package webui

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// Adoption must be unreachable the moment anything is configured: every adopt
// route answers 409, a distinct signal the frontend uses to drop the flow.
func TestAdoptRoutesConflictWhenConfigured(t *testing.T) {
	s := &Server{}
	s.actions.AdoptAllowed = func() error { return errors.New("already configured") }

	cases := []struct {
		name    string
		handler http.HandlerFunc
		method  string
		body    string
	}{
		{"scan", s.handleAdoptScan, http.MethodGet, ""},
		{"inspect", s.handleAdoptInspect, http.MethodPost, "{}"},
		{"test share", s.handleAdoptTestShare, http.MethodPost, "{}"},
		{"adopt", s.handleAdopt, http.MethodPost, "{}"},
	}
	for _, c := range cases {
		req := httptest.NewRequest(c.method, "/x", strings.NewReader(c.body))
		rec := httptest.NewRecorder()
		c.handler(rec, req)
		if rec.Code != http.StatusConflict {
			t.Errorf("%s: got %d, want 409 on a configured machine", c.name, rec.Code)
		}
	}
}

func TestAdoptHandlersWithoutActionsDoNotPanic(t *testing.T) {
	s := &Server{}
	s.actions.AdoptAllowed = func() error { return nil } // gate open, actions unwired

	cases := []struct {
		name    string
		handler http.HandlerFunc
		method  string
		body    string
	}{
		{"scan", s.handleAdoptScan, http.MethodGet, ""},
		{"inspect", s.handleAdoptInspect, http.MethodPost, "{}"},
		{"test share", s.handleAdoptTestShare, http.MethodPost, "{}"},
		{"adopt", s.handleAdopt, http.MethodPost, "{}"},
		{"archive password", s.handleArchivePassword, http.MethodPost, `{"password":"x"}`},
	}
	for _, c := range cases {
		req := httptest.NewRequest(c.method, "/x", strings.NewReader(c.body))
		rec := httptest.NewRecorder()
		c.handler(rec, req)
		if rec.Code != http.StatusServiceUnavailable {
			t.Errorf("%s: got %d, want 503 when the action is unwired", c.name, rec.Code)
		}
	}
}

// With no AdoptAllowed wired at all, the gate itself must degrade to 503.
func TestAdoptGateUnwired(t *testing.T) {
	s := &Server{}
	req := httptest.NewRequest(http.MethodGet, "/x", nil)
	rec := httptest.NewRecorder()
	s.handleAdoptScan(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("got %d, want 503 when AdoptAllowed is unwired", rec.Code)
	}
}

func TestAdoptDecodesRequestAndPassesThrough(t *testing.T) {
	s := &Server{}
	s.actions.AdoptAllowed = func() error { return nil }
	var got AdoptRequest
	s.actions.Adopt = func(req AdoptRequest) (any, error) {
		got = req
		return map[string]any{"ok": true}, nil
	}

	body := `{"source":{"path":"/media/usb"},"continue_as_machine":true,` +
		`"path_remap":{"aaaaa-bbbbb":"/home/new/docs"},"share_passwords":{"nas":""}}`
	req := httptest.NewRequest(http.MethodPost, "/api/adopt", strings.NewReader(body))
	rec := httptest.NewRecorder()
	s.handleAdopt(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("got %d: %s", rec.Code, rec.Body.String())
	}
	if got.Source.Path != "/media/usb" || !got.ContinueAsMachine {
		t.Errorf("decoded request = %+v", got)
	}
	if got.PathRemap["aaaaa-bbbbb"] != "/home/new/docs" {
		t.Errorf("path remap lost: %+v", got.PathRemap)
	}
	// Presence of an empty password must survive decoding — it's a guest share.
	if pw, ok := got.SharePasswords["nas"]; !ok || pw != "" {
		t.Errorf("guest password entry lost: ok=%v pw=%q", ok, pw)
	}
}
