// SPDX-License-Identifier: MIT

package daemon

import (
	"bytes"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/phil9922/backup-maker/internal/config"
	"github.com/phil9922/backup-maker/internal/webui"
)

// The dashboard's request and the setup package's are two types, translated
// field by field. A field dropped in that translation is silent — the wizard
// says three folders and the machine protects one — so drive the real
// translation and check what landed in config.toml.
func TestTheWizardsWholeSelectionReachesSetup(t *testing.T) {
	isolateState(t)
	if err := config.New().Save(); err != nil {
		t.Fatal(err)
	}
	base := t.TempDir()
	code := filepath.Join(base, "code")
	docs := filepath.Join(base, "documents")
	dest := filepath.Join(base, "card")
	for _, dir := range []string{code, docs, dest} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}

	state := &config.State{}
	if err := state.Save(); err != nil {
		t.Fatal(err)
	}
	d := &daemon{log: slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)), state: state}

	out, err := d.createBackup(webui.BackupRequest{
		Folders: []webui.FolderRef{{Path: code}, {Path: docs}},
		Destinations: []webui.Destination{
			{Path: dest, Name: "laptopcard"},
		},
	})
	if err != nil {
		t.Fatalf("creating the backup: %v", err)
	}
	// The names come back one per destination, in the order they were sent: the
	// wizard maps them back by index to watch a paired machine for approval.
	names, _ := out.(map[string]any)["destinations"].([]string)
	if len(names) != 1 || names[0] != "laptopcard" {
		t.Errorf("destinations came back as %v, want the one that was sent", names)
	}

	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Folders) != 2 {
		t.Fatalf("the machine protects %+v, want both folders the wizard sent", cfg.Folders)
	}
	for _, f := range cfg.Folders {
		if !cfg.Mirrored(f.ID) {
			t.Errorf("%q was chosen in the wizard and is backed up by nothing", f.Label)
		}
	}
	if got := cfg.Targets[0].Folders; len(got) != 2 {
		t.Errorf("the destination is scoped to %v; an empty list would mean every folder on the machine", got)
	}
}
