// SPDX-License-Identifier: MIT

package daemon

import (
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/phil9922/backup-maker/internal/config"
	"github.com/phil9922/backup-maker/internal/syncthing"
)

// fakeEngine serves the folder list a receiving machine's engine would, and
// records every folder the daemon asked it to revert. Reverting is destructive,
// so "which id reached /rest/db/revert" is the only thing worth asserting on.
func fakeEngine(t *testing.T, folders []map[string]any, reverted *[]string) *syncthing.Client {
	t.Helper()
	mux := http.NewServeMux()
	write := func(w http.ResponseWriter, v any) { _ = json.NewEncoder(w).Encode(v) }

	mux.HandleFunc("/rest/system/status", func(w http.ResponseWriter, r *http.Request) {
		write(w, map[string]any{"myID": "MYSELF"})
	})
	mux.HandleFunc("/rest/config/folders", func(w http.ResponseWriter, r *http.Request) {
		write(w, folders)
	})
	mux.HandleFunc("/rest/config/devices", func(w http.ResponseWriter, r *http.Request) {
		write(w, []map[string]any{
			{"deviceID": "MYSELF", "name": "attic-pi"},
			{"deviceID": "SENDER", "name": "workstation"},
		})
	})
	mux.HandleFunc("/rest/db/revert", func(w http.ResponseWriter, r *http.Request) {
		*reverted = append(*reverted, r.URL.Query().Get("folder"))
		write(w, map[string]bool{"ok": true})
	})

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	_, port, err := net.SplitHostPort(strings.TrimPrefix(srv.URL, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	n, err := strconv.Atoi(port)
	if err != nil {
		t.Fatal(err)
	}
	return syncthing.NewClient(n, "test-key")
}

// receivingDaemon is a daemon on a machine that accepts backups into
// /srv/backups, wired to a fake engine holding the given folders.
func receivingDaemon(t *testing.T, folders []map[string]any, reverted *[]string) *daemon {
	t.Helper()
	cfg := config.New()
	cfg.Receive = config.Receive{Enabled: true, Root: "/srv/backups"}
	return &daemon{
		log: slog.New(slog.NewTextHandler(io.Discard, nil)),
		cfg: cfg,
		sup: &syncthing.Supervisor{Client: fakeEngine(t, folders, reverted)},
	}
}

func receivedBackup(id, label, path string) map[string]any {
	return map[string]any{
		"id": id, "label": label, "path": path, "type": "receiveonly",
		"devices": []map[string]any{{"deviceID": "MYSELF"}, {"deviceID": "SENDER"}},
	}
}

// The one folder that may be reverted: received here, and receive-only.
func TestRevertFolderRevertsAReceivedBackup(t *testing.T) {
	var reverted []string
	d := receivingDaemon(t, []map[string]any{
		receivedBackup("f1", "code", "/srv/backups/workstation/code"),
	}, &reverted)

	if err := d.revertFolder("f1"); err != nil {
		t.Fatalf("revertFolder: %v", err)
	}
	if len(reverted) != 1 || reverted[0] != "f1" {
		t.Errorf("engine was asked to revert %v, want [f1]", reverted)
	}
}

// A crafted or stale request must never reach the engine. Reverting a folder
// this machine SENDS asks the engine to overwrite the user's own files with a
// remote copy and delete anything the remote hasn't got — the one outcome a
// backup tool must make impossible — so the daemon decides what may be
// reverted from its own view of the engine, never from the request.
func TestRevertFolderRefusesAnythingNotReceivedHere(t *testing.T) {
	folders := []map[string]any{
		receivedBackup("f1", "code", "/srv/backups/workstation/code"),
		// This machine's own data, sent to another machine — and deliberately
		// sitting under the receive root, so only the TYPE distinguishes it.
		{"id": "mine", "label": "my-photos", "path": "/srv/backups/decoy", "type": "sendonly"},
		// Receive-only, but nothing to do with backups received here.
		{"id": "elsewhere", "label": "other", "path": "/mnt/other", "type": "receiveonly"},
		// A sibling directory whose name merely starts with the receive root: a
		// plain string prefix would have accepted this one.
		{"id": "sibling", "label": "nearby", "path": "/srv/backups-mine/code", "type": "receiveonly"},
	}
	for _, id := range []string{"mine", "elsewhere", "sibling", "unknown", ""} {
		t.Run("id "+id, func(t *testing.T) {
			var reverted []string
			d := receivingDaemon(t, folders, &reverted)
			err := d.revertFolder(id)
			if err == nil {
				t.Fatalf("revertFolder(%q) was accepted", id)
			}
			if !strings.Contains(err.Error(), "not a backup this machine receives") {
				t.Errorf("unhelpful refusal for %q: %v", id, err)
			}
			if len(reverted) != 0 {
				t.Errorf("the engine was asked to revert %v anyway", reverted)
			}
		})
	}
}

// A machine that isn't receiving backups has nothing to revert, and must not
// start deleting files on the strength of a request alone.
func TestRevertFolderRefusesWhenNotReceiving(t *testing.T) {
	var reverted []string
	d := receivingDaemon(t, []map[string]any{
		receivedBackup("f1", "code", "/srv/backups/workstation/code"),
	}, &reverted)
	d.cfg.Receive = config.Receive{}

	if err := d.revertFolder("f1"); err == nil {
		t.Fatal("revert was accepted on a machine that receives nothing")
	}
	if len(reverted) != 0 {
		t.Errorf("the engine was asked to revert %v", reverted)
	}
}

// Without the engine there is nothing to ask; that is a plain explanation, not
// a nil-pointer panic that takes the daemon down.
func TestRevertFolderWithoutTheEngine(t *testing.T) {
	cfg := config.New()
	cfg.Receive = config.Receive{Enabled: true, Root: "/srv/backups"}
	d := &daemon{log: slog.New(slog.NewTextHandler(io.Discard, nil)), cfg: cfg}
	if err := d.revertFolder("f1"); err == nil {
		t.Fatal("revert was accepted with no sync engine running")
	}
}
