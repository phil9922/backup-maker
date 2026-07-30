// SPDX-License-Identifier: MIT

package config

import (
	"strings"
	"testing"

	"github.com/phil9922/backup-maker/internal/testpath"
)

// THE GUARANTEE: two destinations cannot share a name.
//
// The name is a KEY — for the marker UUID that says which storage this is, for
// the share password, and for every folder's last-synced clock. Two targets
// called the same thing therefore share all three: one of them ends up refused
// as foreign storage, or handed the other's password. Nothing refused this
// before, and config.toml is a file people edit — now that renaming is offered
// from the dashboard and the CLI, it is also a mistake a person can make on
// purpose.
func TestTwoDestinationsCannotShareAName(t *testing.T) {
	cfg := New()
	cfg.General.MachineName = "laptop"
	cfg.Targets = []Target{
		{Type: "drive", Name: "backups", Path: testpath.Abs("/mnt/a"), Folders: []string{}},
		{Type: "share", Name: "backups", URL: "//pi/backups", Folders: []string{}},
	}
	err := cfg.Validate()
	if err == nil {
		t.Fatal("a config with two destinations called \"backups\" was accepted")
	}
	if !strings.Contains(err.Error(), "backups") {
		t.Errorf("the error does not name the destination: %v", err)
	}
	if !strings.Contains(err.Error(), "rename") {
		t.Errorf("the error does not say what to do about it: %v", err)
	}
}

func TestADestinationWithNoNameIsRefused(t *testing.T) {
	cfg := New()
	cfg.Targets = []Target{{Type: "drive", Name: "", Path: testpath.Abs("/mnt/a"), Folders: []string{}}}
	if err := cfg.Validate(); err == nil {
		t.Error("a destination with no name was accepted; nothing could refer to it")
	}
}

// Two names that differ only in case are ALLOWED here, deliberately, and the
// asymmetry is worth stating: setup refuses to create one, because a person
// cannot tell "backups" from "Backups" in any sentence — but this function is
// what an existing config has to get past on every load. Refusing a pair that
// has been working for months would stop a machine backing up over a matter of
// presentation.
func TestACaseOnlyDifferenceStillLoads(t *testing.T) {
	cfg := New()
	cfg.General.MachineName = "laptop"
	cfg.Targets = []Target{
		{Type: "drive", Name: "backups", Path: testpath.Abs("/mnt/a"), Folders: []string{}},
		{Type: "drive", Name: "Backups", Path: testpath.Abs("/mnt/b"), Folders: []string{}},
	}
	if err := cfg.Validate(); err != nil {
		t.Errorf("a config that has been working was refused on load: %v", err)
	}
}
