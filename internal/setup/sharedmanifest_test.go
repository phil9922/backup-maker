// SPDX-License-Identifier: MIT

package setup

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/phil9922/backup-maker/internal/config"
	"github.com/phil9922/backup-maker/internal/localmirror"
)

// configFor is a minimal machine: one folder, one drive destination.
func configFor(machine string, folders ...string) *config.Config {
	cfg := config.New()
	cfg.General.MachineName = machine
	for _, label := range folders {
		cfg.Folders = append(cfg.Folders, config.Folder{
			ID: label + "-id", Label: label, Path: "/home/somebody/" + label,
		})
	}
	cfg.Targets = []config.Target{{Type: "drive", Name: "card", Path: "/media/card", Folders: []string{}}}
	return cfg
}

// THE BUG: one manifest at the destination root meant one machine's worth of
// configuration per drive. Two computers sharing a drive overwrote each other's
// on every cycle, so "restore this machine" could only ever rebuild whichever
// of them wrote last — and the other's backups sat there, complete, described
// by nothing.
func TestTwoMachinesOnOneDriveKeepBothManifests(t *testing.T) {
	root := t.TempDir()
	b := localmirror.NewLocalFS(root)

	if err := WriteManifest(b, configFor("oldbox", "Documents"), nil, "install-oldbox"); err != nil {
		t.Fatalf("writing oldbox's manifest: %v", err)
	}
	if err := WriteManifest(b, configFor("attic-pi", "Photos", "Music"), nil, "install-pi"); err != nil {
		t.Fatalf("writing attic-pi's manifest: %v", err)
	}

	found, err := ListManifests(b)
	if err != nil {
		t.Fatalf("ListManifests: %v", err)
	}
	if len(found) != 2 {
		t.Fatalf("a drive holding two machines reported %d manifests: %+v", len(found), found)
	}

	byName := map[string]*Manifest{}
	for _, f := range found {
		byName[f.MachineName] = f.Manifest
	}
	if m := byName["oldbox"]; m == nil || len(m.Folders) != 1 {
		t.Errorf("oldbox's manifest was lost or overwritten: %+v", m)
	}
	if m := byName["attic-pi"]; m == nil || len(m.Folders) != 2 {
		t.Errorf("attic-pi's manifest was lost or overwritten: %+v", m)
	}

	// And each is adoptable by name, which is what the picker in the wizard and
	// the CLI prompt both depend on.
	for name, want := range map[string]int{"oldbox": 1, "attic-pi": 2} {
		m, err := ReadManifestFor(b, name)
		if err != nil {
			t.Errorf("ReadManifestFor(%q): %v", name, err)
			continue
		}
		if len(m.Folders) != want {
			t.Errorf("%s: got %d folders, want %d", name, len(m.Folders), want)
		}
	}
}

// A destination written by an older backup-maker has its manifest at the root
// and nothing else. It must stay adoptable — a program whose whole purpose is
// getting data back cannot make a drive unreadable by being upgraded.
func TestAnOldDriveIsStillAdoptable(t *testing.T) {
	root := t.TempDir()
	b := localmirror.NewLocalFS(root)
	writeLegacyManifest(t, root, configFor("oldbox", "Documents"))

	found, err := ListManifests(b)
	if err != nil {
		t.Fatalf("ListManifests: %v", err)
	}
	if len(found) != 1 || found[0].MachineName != "oldbox" || !found[0].Legacy {
		t.Fatalf("a pre-upgrade drive did not read as one legacy machine: %+v", found)
	}
	m, err := ReadManifest(b)
	if err != nil {
		t.Fatalf("ReadManifest on a pre-upgrade drive: %v", err)
	}
	if m.MachineName != "oldbox" || len(m.Folders) != 1 {
		t.Errorf("the legacy manifest did not survive the read: %+v", m)
	}
}

// After an upgrade the root file is a fossil: the same machine now writes a
// current one inside its own directory. Adopting from the fossil would restore
// a configuration that was superseded — so it is hidden. It is NOT deleted:
// removing it would stop an older binary adopting the drive, and tidying our
// own files off a destination root is not a thing this program does.
func TestAPerMachineManifestHidesTheStaleRootOne(t *testing.T) {
	root := t.TempDir()
	b := localmirror.NewLocalFS(root)

	stale := configFor("oldbox", "Documents")
	writeLegacyManifest(t, root, stale)

	current := configFor("oldbox", "Documents", "Photos", "Music")
	if err := WriteManifest(b, current, nil, "install-oldbox"); err != nil {
		t.Fatalf("WriteManifest: %v", err)
	}

	found, err := ListManifests(b)
	if err != nil {
		t.Fatalf("ListManifests: %v", err)
	}
	if len(found) != 1 {
		t.Fatalf("the superseded root manifest was still offered: %+v", found)
	}
	if found[0].Legacy {
		t.Error("the fossil won over the current manifest")
	}
	if len(found[0].Manifest.Folders) != 3 {
		t.Errorf("adoption would have restored the stale configuration: %d folders, want 3",
			len(found[0].Manifest.Folders))
	}

	// Left where it is, for the older binary that may still read this drive.
	if _, err := os.Stat(filepath.Join(root, ManifestName)); err != nil {
		t.Errorf("the legacy manifest was deleted from the destination root: %v", err)
	}
}

// A drive holding two machines must let the caller say which one, and must not
// quietly pick for them.
func TestAdoptingAsharedDriveAsksWhichComputer(t *testing.T) {
	root := t.TempDir()
	b := localmirror.NewLocalFS(root)
	if err := WriteManifest(b, configFor("oldbox", "Documents"), nil, "install-oldbox"); err != nil {
		t.Fatal(err)
	}
	// Written second, so "newest wins" would choose this one.
	time.Sleep(2 * time.Millisecond)
	if err := WriteManifest(b, configFor("attic-pi", "Photos", "Music"), nil, "install-pi"); err != nil {
		t.Fatal(err)
	}

	insp, err := InspectSource(AdoptSource{Path: root})
	if err != nil {
		t.Fatalf("InspectSource: %v", err)
	}
	if len(insp.Machines) != 2 {
		t.Fatalf("inspection offered %d machines to choose from, want 2: %+v", len(insp.Machines), insp.Machines)
	}

	named, err := InspectSource(AdoptSource{Path: root, Machine: "oldbox"})
	if err != nil {
		t.Fatalf("InspectSource(oldbox): %v", err)
	}
	if named.MachineName != "oldbox" || len(named.Folders) != 1 {
		t.Errorf("naming a machine did not select it: got %q with %d folders",
			named.MachineName, len(named.Folders))
	}

	if _, err := InspectSource(AdoptSource{Path: root, Machine: "a-computer-that-is-not-here"}); err == nil {
		t.Error("adopting a machine the destination does not hold was allowed")
	}
}

// scanRoots is what "Restore this machine" renders from. A drive holding two
// machines must offer both, or one of them is unreachable from the only screen
// that would have found it.
func TestScanRootsFindsEveryMachineOnADrive(t *testing.T) {
	root := t.TempDir()
	b := localmirror.NewLocalFS(root)
	if err := WriteManifest(b, configFor("oldbox", "Documents"), nil, "install-oldbox"); err != nil {
		t.Fatal(err)
	}
	if err := WriteManifest(b, configFor("attic-pi", "Photos"), nil, "install-pi"); err != nil {
		t.Fatal(err)
	}

	got := scanRoots([]string{root})
	if len(got) != 2 {
		t.Fatalf("scan found %d candidates on a two-machine drive, want 2: %+v", len(got), got)
	}
	names := map[string]bool{}
	for _, c := range got {
		names[c.MachineName] = true
		if c.Path != root {
			t.Errorf("candidate points at %q, want the destination root %q", c.Path, root)
		}
	}
	if !names["oldbox"] || !names["attic-pi"] {
		t.Errorf("a machine was missing from the scan: %+v", got)
	}
}

// writeLegacyManifest puts a version-1 manifest at the destination root, the
// way every backup-maker before this change did.
func writeLegacyManifest(t *testing.T, root string, cfg *config.Config) {
	t.Helper()
	m := BuildManifest(cfg, nil, "", time.Now().Add(-time.Hour))
	m.Version = 1
	m.InstallID = ""
	data, err := jsonIndent(m)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ManifestName), data, 0o644); err != nil {
		t.Fatal(err)
	}
}

func jsonIndent(v any) ([]byte, error) { return json.MarshalIndent(v, "", "  ") }
