// SPDX-License-Identifier: MIT

package webui

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// The wizard sends the WHOLE selection in one request, and every entry of it has
// to arrive: a folder dropped between the browser and the daemon is a folder the
// user was told was protected and is not.
func TestEveryChosenFolderReachesTheDaemon(t *testing.T) {
	s := &Server{}
	var got BackupRequest
	s.actions.CreateBackup = func(req BackupRequest) (any, error) {
		got = req
		return map[string]any{"destinations": []string{"laptopcard"}}, nil
	}
	body := `{"folders":[{"path":"/home/alex/code","folder_id":""},
	                     {"path":"/home/alex/Documents","folder_id":"kqz3d-8xh2p"}],
	          "mode":"timed","destinations":[{"path":"/media/alex/SDCARD"}],
	          "archive":{"name":"nightly","every":"daily","keep":5,"password":"pw"}}`
	rec := httptest.NewRecorder()
	s.handleCreateBackup(rec, httptest.NewRequest(http.MethodPost, "/api/backups", strings.NewReader(body)))

	if rec.Code != http.StatusOK {
		t.Fatalf("got %d: %s", rec.Code, rec.Body.String())
	}
	if len(got.Folders) != 2 {
		t.Fatalf("the daemon was asked for %+v, want both chosen folders", got.Folders)
	}
	if got.Folders[0].Path != "/home/alex/code" || got.Folders[0].FolderID != "" {
		t.Errorf("first folder arrived as %+v", got.Folders[0])
	}
	// An id names a folder that already exists — this is a second KIND of backup
	// for it, not a second copy of it.
	if got.Folders[1].FolderID != "kqz3d-8xh2p" {
		t.Errorf("second folder arrived as %+v, losing the id of the folder it means", got.Folders[1])
	}
	if got.Mode != "timed" || got.Archive == nil || got.Archive.Every != "daily" {
		t.Errorf("the schedule that IS the protection did not arrive intact: %+v", got.Archive)
	}
}

// The older single-folder body is still the whole contract for anything that
// only ever protects one folder.
func TestTheSingleFolderBodyStillReachesTheDaemon(t *testing.T) {
	s := &Server{}
	var got BackupRequest
	s.actions.CreateBackup = func(req BackupRequest) (any, error) {
		got = req
		return map[string]any{"destinations": []string{"laptopcard"}}, nil
	}
	body := `{"path":"/home/alex/code","mode":"incremental","destinations":[{"path":"/media/alex/SDCARD"}]}`
	rec := httptest.NewRecorder()
	s.handleCreateBackup(rec, httptest.NewRequest(http.MethodPost, "/api/backups", strings.NewReader(body)))

	if rec.Code != http.StatusOK {
		t.Fatalf("got %d: %s", rec.Code, rec.Body.String())
	}
	if len(got.Folders) != 0 || got.Path != "/home/alex/code" {
		t.Errorf("the daemon was asked for %+v / %q, want the single folder form", got.Folders, got.Path)
	}
}
