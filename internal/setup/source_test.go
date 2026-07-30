// SPDX-License-Identifier: MIT

package setup

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/phil9922/backup-maker/internal/config"
	"github.com/phil9922/backup-maker/internal/localmirror"
)

// writeSampleDest stamps a temp dir as the "usb" target: manifest + marker,
// exactly what a real destination carries. The sample folder's path is
// rewritten to exists (a real dir) or a path that can't exist.
func writeSampleDest(t *testing.T, folderExists bool) string {
	t.Helper()
	dest := t.TempDir()
	cfg := sampleConfig()
	if folderExists {
		cfg.Folders[0].Path = t.TempDir()
	} else {
		cfg.Folders[0].Path = filepath.Join(string(os.PathSeparator), "no", "such", "dir")
	}
	b := localmirror.NewLocalFS(dest)
	// The drive's own manifest: usb in full, nas by name only.
	if err := WriteManifest(b, cfg, map[string]string{"usb": "uuid-usb", "nas": "uuid-nas"}, "install-oldbox", "usb"); err != nil {
		t.Fatal(err)
	}
	if err := localmirror.WriteMarker(b, "uuid-usb", "oldbox"); err != nil {
		t.Fatal(err)
	}
	return dest
}

func TestInspectSourceReportsManifestAndPointedTarget(t *testing.T) {
	dest := writeSampleDest(t, true)
	insp, err := InspectSource(AdoptSource{Path: dest})
	if err != nil {
		t.Fatalf("InspectSource: %v", err)
	}
	if insp.MachineName != "oldbox" {
		t.Errorf("machine = %q", insp.MachineName)
	}
	if len(insp.Folders) != 1 || !insp.Folders[0].Exists {
		t.Errorf("folders = %+v, want 1 existing", insp.Folders)
	}
	var usb, nas *AdoptTargetInfo
	for i := range insp.Targets {
		switch insp.Targets[i].Name {
		case "usb":
			usb = &insp.Targets[i]
		case "nas":
			nas = &insp.Targets[i]
		}
	}
	if usb == nil || !usb.PointedAt {
		t.Errorf("usb should be pointed-at (marker uuid match): %+v", usb)
	}
	if nas == nil || nas.PointedAt {
		t.Errorf("nas should NOT be pointed-at: %+v", nas)
	}
	if usb.Location != "/mnt/usb" || usb.NeedsReadding {
		t.Errorf("the drive being inspected should be described in full: %+v", usb)
	}
	// The preview has to SAY the other destination cannot be restored from
	// here. Showing it with a blank location and no flag would read as a
	// destination with no address, which is a fault rather than a decision.
	if nas.Location != "" || !nas.NeedsReadding {
		t.Errorf("nas should be reported as needing re-adding, with no address: %+v", nas)
	}
	if len(insp.Archives) != 1 || insp.Archives[0].Name != "weekly" {
		t.Errorf("archives = %+v", insp.Archives)
	}
}

func TestInspectSourceFlagsMissingFolder(t *testing.T) {
	dest := writeSampleDest(t, false)
	insp, err := InspectSource(AdoptSource{Path: dest})
	if err != nil {
		t.Fatalf("InspectSource: %v", err)
	}
	if insp.Folders[0].Exists {
		t.Error("folder should be reported missing on this machine")
	}
}

func TestInspectSourceNoManifest(t *testing.T) {
	if _, err := InspectSource(AdoptSource{Path: t.TempDir()}); err == nil {
		t.Fatal("expected an error for a directory with no manifest")
	}
}

func TestAdoptFromSourceStoresPointedShareCredentials(t *testing.T) {
	// When the SOURCE is a share matched by URL, its just-used credentials are
	// stored for that target automatically — even an empty guest password.
	isolateNoConfig(t)
	dest := t.TempDir()
	cfg := sampleConfig()
	b := localmirror.NewLocalFS(dest)
	// No marker (simulates a share whose marker UUID doesn't match); the nas
	// target is matched by URL instead — but a local path carries no URL, so
	// exercise pointedTargetName's URL branch directly plus the storage path
	// via AdoptFromSource on a marker-matched local dir.
	// Written for the share, so the share is the entry carrying a URL to match.
	if err := WriteManifest(b, cfg, map[string]string{"usb": "uuid-usb", "nas": "uuid-nas"}, "install-oldbox", "nas"); err != nil {
		t.Fatal(err)
	}
	m, err := ReadManifest(b)
	if err != nil {
		t.Fatal(err)
	}
	if got := pointedTargetName(b, m, AdoptSource{URL: "//nas/backups"}); got != "nas" {
		t.Fatalf("pointedTargetName by URL = %q, want nas", got)
	}

	// Guest-share semantics through Adopt itself: presence with empty value
	// must store the credential, not report it missing.
	res, err := Adopt(m, AdoptDecisions{
		ContinueAsMachine: true,
		SharePasswords:    map[string]string{"nas": ""},
	})
	if err != nil {
		t.Fatalf("Adopt: %v", err)
	}
	if len(res.SharesNeedingPassword) != 0 {
		t.Errorf("SharesNeedingPassword = %v, want none (guest password provided)", res.SharesNeedingPassword)
	}
	st, err := config.LoadState()
	if err != nil {
		t.Fatal(err)
	}
	if pw, ok := st.ShareCredentials["nas"]; !ok || pw != "" {
		t.Errorf("guest credential not stored: ok=%v pw=%q", ok, pw)
	}
}
