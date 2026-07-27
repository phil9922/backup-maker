// SPDX-License-Identifier: MIT

package setup

import (
	"strings"
	"testing"
)

const goodDeviceID = "ABCDEFG-HIJKLMN-OPQRSTU-VWXYZ01-2345678-9ABCDEF-GHIJKLM-NOPQRST"

// The CLI and the dashboard must accept and refuse exactly the same things,
// which is the entire reason this rule lives in one place.
func TestNormalizeDeviceID(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"already clean", goodDeviceID, goodDeviceID},
		{"lowercase paste", strings.ToLower(goodDeviceID), goodDeviceID},
		{"surrounding whitespace", "  " + goodDeviceID + "\n", goodDeviceID},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := NormalizeDeviceID(c.in)
			if err != nil {
				t.Fatalf("NormalizeDeviceID(%q): %v", c.in, err)
			}
			if got != c.want {
				t.Errorf("got %q, want %q", got, c.want)
			}
		})
	}

	bad := map[string]string{
		"empty":            "",
		"half a paste":     "ABCDEFG-HIJKLMN-OPQRSTU",
		"no groups at all": strings.ReplaceAll(goodDeviceID, "-", ""),
		"a machine name":   "attic-pi",
	}
	for name, in := range bad {
		t.Run(name, func(t *testing.T) {
			if _, err := NormalizeDeviceID(in); err == nil {
				t.Errorf("NormalizeDeviceID(%q) was accepted", in)
			}
		})
	}
}

// The wizard sends a pasted device ID straight through to CreateBackup, so the
// same rule has to hold on the way in — otherwise a mistyped ID is saved as a
// destination that can never connect.
func TestCreateBackupValidatesAPastedDeviceID(t *testing.T) {
	isolate(t)
	src := mustDir(t, t.TempDir(), "src")
	if _, _, err := CreateBackup(BackupRequest{
		Path:         src,
		Destinations: []Destination{{DeviceID: "ABCDEFG-HIJ", Name: "attic-pi"}},
	}); err == nil {
		t.Fatal("a truncated device ID was accepted as a destination")
	}
	cfg := load(t)
	if len(cfg.Folders) != 0 || len(cfg.Targets) != 0 {
		t.Error("the rejected backup still wrote configuration")
	}
}

// A machine pasted in lowercase is the same machine: it must be stored in the
// form syncthing uses, or the target would never match the device it names.
func TestCreateBackupStoresANormalizedDeviceID(t *testing.T) {
	isolate(t)
	src := mustDir(t, t.TempDir(), "src")
	_, targets, err := CreateBackup(BackupRequest{
		Path:         src,
		Destinations: []Destination{{DeviceID: strings.ToLower(goodDeviceID), Name: "attic-pi"}},
	})
	if err != nil {
		t.Fatalf("CreateBackup: %v", err)
	}
	if len(targets) != 1 || targets[0].DeviceID != goodDeviceID {
		t.Fatalf("stored target = %+v, want the uppercased ID", targets)
	}
	if targets[0].Name != "attic-pi" {
		t.Errorf("target name = %q, want the name the user gave it", targets[0].Name)
	}
}

// Backing a SECOND folder up to a machine that is already a destination must
// send it to that machine, not stand up a duplicate target for the same
// computer (which would leave two names for one device in the engine's config).
func TestCreateBackupReusesAnAlreadyPairedMachine(t *testing.T) {
	isolate(t)
	base := t.TempDir()
	first := mustDir(t, base, "first")
	second := mustDir(t, base, "second")

	folderA, _, err := CreateBackup(BackupRequest{
		Path:         first,
		Destinations: []Destination{{DeviceID: goodDeviceID, Name: "attic-pi"}},
	})
	if err != nil {
		t.Fatalf("first backup: %v", err)
	}
	folderB, targets, err := CreateBackup(BackupRequest{
		Path:         second,
		Destinations: []Destination{{DeviceID: goodDeviceID, Name: "attic-pi-again"}},
	})
	if err != nil {
		t.Fatalf("second backup: %v", err)
	}
	if len(targets) != 1 || targets[0].Name != "attic-pi" {
		t.Fatalf("second backup created %+v, want the existing target", targets)
	}

	cfg := load(t)
	devices := 0
	for _, tg := range cfg.Targets {
		if tg.Type == "device" {
			devices++
		}
	}
	if devices != 1 {
		t.Fatalf("got %d device targets, want the one machine", devices)
	}
	if len(cfg.Targets[0].Folders) != 2 ||
		cfg.Targets[0].Folders[0] != folderA.ID || cfg.Targets[0].Folders[1] != folderB.ID {
		t.Errorf("target folders = %v, want both folders attached", cfg.Targets[0].Folders)
	}
}
