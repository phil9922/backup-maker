// SPDX-License-Identifier: MIT

package webui

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
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
		{"remove target", s.handleRemoveTarget, http.MethodDelete, ""},
		{"add archive", s.handleAddArchive, http.MethodPost, "{}"},
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
