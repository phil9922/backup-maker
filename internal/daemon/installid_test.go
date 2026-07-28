// SPDX-License-Identifier: MIT

package daemon

import (
	"bytes"
	"context"
	"log/slog"
	"testing"

	"github.com/phil9922/backup-maker/internal/config"
	"github.com/phil9922/backup-maker/internal/localmirror"
)

// applyConfig re-reads state.json every two seconds, because setup commands
// write UUIDs and credentials there while the daemon runs. That swap already
// has to carry the IPC token across by hand ("never rotate a live token").
//
// The install id needs the same care for a worse reason: it is what every claim
// file on every destination is keyed on. A daemon that lost it would stop
// recognising its OWN directories on its OWN drives, and would refuse to back
// up to them — silently, and on a timer, two seconds after starting.
func TestTheInstallIDSurvivesADaemonStateRewrite(t *testing.T) {
	isolateState(t)
	root := t.TempDir()
	if err := localmirror.WriteMarkerAt(root, "card-uuid", "workstation"); err != nil {
		t.Fatal(err)
	}
	// What a setup command leaves on disk: no install id, because it was written
	// by an older binary or by a command that had no reason to mint one.
	onDisk := &config.State{DriveTargetUUIDs: map[string]string{"card": "card-uuid"}}
	if err := onDisk.Save(); err != nil {
		t.Fatalf("seeding state: %v", err)
	}

	cfg := &config.Config{
		General: config.General{MachineName: "workstation"},
		Targets: []config.Target{{Type: "drive", Name: "card", Path: root}},
	}
	inMemory := &config.State{
		IPCToken:         "live-token",
		InstallID:        "this-installation",
		DriveTargetUUIDs: map[string]string{"card": "card-uuid"},
	}
	d := &daemon{
		log:   slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)),
		state: inMemory,
		cfg:   cfg,
		marks: newSyncMarks(inMemory),
	}

	d.applyConfig(context.Background(), cfg)

	d.mu.Lock()
	got := d.state
	d.mu.Unlock()

	if got.InstallID != "this-installation" {
		t.Errorf("the install id became %q after a state re-read; every claim this machine holds would stop reading as its own", got.InstallID)
	}
	if got.IPCToken != "live-token" {
		t.Errorf("the live IPC token was rotated to %q by a state re-read", got.IPCToken)
	}
}
