// SPDX-License-Identifier: MIT

package webui

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/phil9922/backup-maker/internal/status"
)

// The typed confirmation reaches the daemon verbatim. The check that matters
// is the one nearest the deletion, so what the page sent has to arrive intact
// rather than being interpreted on the way.
func TestDeleteRetiredPassesTheTypedConfirmationThrough(t *testing.T) {
	s := &Server{}
	var gotID, gotConfirm string
	s.actions.DeleteRetiredBackups = func(id, confirm string) (any, error) {
		gotID, gotConfirm = id, confirm
		return map[string]any{"ok": true}, nil
	}

	req := httptest.NewRequest(http.MethodPost, "/api/retired/f1/delete", strings.NewReader(`{"confirm":"development"}`))
	req.SetPathValue("id", "f1")
	rec := httptest.NewRecorder()
	s.handleDeleteRetiredBackups(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("got %d, want 200: %s", rec.Code, rec.Body.String())
	}
	if gotID != "f1" || gotConfirm != "development" {
		t.Errorf("daemon received id=%q confirm=%q", gotID, gotConfirm)
	}
}

// An empty confirmation is refused before the action is ever called. Deleting
// somebody's backups is not something a request gets to do by omission.
func TestDeleteRetiredWithoutAConfirmationIsRefused(t *testing.T) {
	s := &Server{}
	called := false
	s.actions.DeleteRetiredBackups = func(string, string) (any, error) {
		called = true
		return nil, nil
	}

	for _, body := range []string{`{}`, `{"confirm":""}`, `{"confirm":"   "}`} {
		req := httptest.NewRequest(http.MethodPost, "/api/retired/f1/delete", strings.NewReader(body))
		req.SetPathValue("id", "f1")
		rec := httptest.NewRecorder()
		s.handleDeleteRetiredBackups(rec, req)
		if rec.Code != http.StatusUnprocessableEntity {
			t.Errorf("body %s got %d, want 422", body, rec.Code)
		}
	}
	if called {
		t.Error("the delete action was invoked without a confirmation")
	}
}

// A refusal from the daemon is passed on rather than swallowed into a success.
func TestDeleteRetiredSurfacesTheDaemonsRefusal(t *testing.T) {
	s := &Server{}
	s.actions.DeleteRetiredBackups = func(string, string) (any, error) {
		return nil, errors.New("that is not the storage backup-maker knows")
	}

	req := httptest.NewRequest(http.MethodPost, "/api/retired/f1/delete", strings.NewReader(`{"confirm":"development"}`))
	req.SetPathValue("id", "f1")
	rec := httptest.NewRecorder()
	s.handleDeleteRetiredBackups(rec, req)

	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("got %d, want 422", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "not the storage") {
		t.Errorf("the reason did not reach the browser: %s", rec.Body.String())
	}
}

// FORGET IS NOT DELETE. A route mix-up here deletes somebody's backups when
// they asked only to stop being reminded of them, so the two are proven to be
// wired to different actions.
func TestForgetRetiredIsADeleteThatTouchesNoFiles(t *testing.T) {
	s := &Server{}
	forgot := ""
	deleted := false
	s.actions.ForgetRetired = func(id string) error { forgot = id; return nil }
	s.actions.DeleteRetiredBackups = func(string, string) (any, error) {
		deleted = true
		return nil, nil
	}

	req := httptest.NewRequest(http.MethodDelete, "/api/retired/f1", nil)
	req.SetPathValue("id", "f1")
	rec := httptest.NewRecorder()
	s.handleForgetRetired(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("got %d, want 200", rec.Code)
	}
	if forgot != "f1" {
		t.Errorf("forget received %q", forgot)
	}
	if deleted {
		t.Fatal("forgetting a record called the action that deletes backups")
	}
}

func TestReenablePassesTheFolderIDThrough(t *testing.T) {
	s := &Server{}
	got := ""
	s.actions.ReenableFolder = func(id string) (any, error) {
		got = id
		return map[string]any{"ok": true}, nil
	}

	req := httptest.NewRequest(http.MethodPost, "/api/retired/f1/reenable", nil)
	req.SetPathValue("id", "f1")
	rec := httptest.NewRecorder()
	s.handleReenableFolder(rec, req)

	if rec.Code != http.StatusOK || got != "f1" {
		t.Errorf("got %d, id %q", rec.Code, got)
	}
}

// Unwired actions answer rather than panic, the same as every other route.
func TestRetiredHandlersWithoutActionsDoNotPanic(t *testing.T) {
	s := &Server{}
	cases := []struct {
		name    string
		handler http.HandlerFunc
		method  string
		body    string
	}{
		{"reenable", s.handleReenableFolder, http.MethodPost, ""},
		{"delete", s.handleDeleteRetiredBackups, http.MethodPost, `{"confirm":"x"}`},
		{"forget", s.handleForgetRetired, http.MethodDelete, ""},
	}
	for _, c := range cases {
		req := httptest.NewRequest(c.method, "/x", strings.NewReader(c.body))
		rec := httptest.NewRecorder()
		c.handler(rec, req)
		if rec.Code != http.StatusServiceUnavailable {
			t.Errorf("%s: got %d, want 503", c.name, rec.Code)
		}
	}
}

// The network view is a read-only page for phones on the wifi. A stopped
// folder's path, and the destinations still holding its copies, are exactly
// the reconnaissance it withholds — and it may never reach the one action in
// this program that deletes a backup.
func TestNetworkViewIsNotToldAboutStoppedBackups(t *testing.T) {
	m := status.Model{
		MachineName: "workstation",
		Retired: []status.RetiredInfo{{
			ID:    "f1",
			Label: "development",
			Path:  "/home/alex/code",
			Copies: []status.RetiredCopyInfo{{
				Target: "laptocard", Type: "drive", Location: "/media/alex/SDCARD",
				DestPath: "workstation/development",
			}},
		}},
	}

	raw, err := json.Marshal(RedactForNetwork(m))
	if err != nil {
		t.Fatal(err)
	}
	got := string(raw)
	if strings.Contains(got, "retired") {
		t.Errorf("the stopped-folder block was published to the network view:\n%s", got)
	}
	for _, leak := range []string{"/home/alex/code", "/media/alex/SDCARD", "workstation/development"} {
		if strings.Contains(got, leak) {
			t.Errorf("%q reached the network view:\n%s", leak, got)
		}
	}
}
