// SPDX-License-Identifier: MIT

package setup

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/phil9922/backup-maker/internal/config"
	"github.com/phil9922/backup-maker/internal/localmirror"
)

// Creating a directory on a destination is only ever done because somebody
// asked for one by name. The typed-path box and the CLI's bare form keep their
// old meaning — the directory must already exist — because a path typed by hand
// is the one most likely to have a typo in it.
func TestABackupFolderOnADriveIsCreatedOnlyWhenAskedFor(t *testing.T) {
	isolate(t)
	drive := t.TempDir()
	cfg := config.New()
	cfg.General.MachineName = "my-laptop"

	target := filepath.Join(drive, "backups")
	if _, err := AppendDriveTarget(cfg, target, "card"); err == nil {
		t.Fatal("a folder that does not exist was accepted, and silently created, by the path that never creates")
	}
	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Fatalf("the refusing path created the directory anyway: %v", err)
	}

	got, err := AppendDriveTargetIn(cfg, target, "card", true, false)
	if err != nil {
		t.Fatalf("asking for the folder to be created was refused: %v", err)
	}
	if fi, err := os.Stat(target); err != nil || !fi.IsDir() {
		t.Fatalf("the folder was not created: %v", err)
	}
	if got.Path != target {
		t.Errorf("target path = %q, want %q", got.Path, target)
	}
	// It is a destination in its own right, and the drive around it is untouched.
	if _, err := os.Stat(filepath.Join(target, localmirror.MarkerName)); err != nil {
		t.Errorf("the folder was not stamped as a destination: %v", err)
	}
	if _, err := os.Stat(filepath.Join(drive, localmirror.MarkerName)); err == nil {
		t.Error("the drive root was stamped; the rest of the drive was supposed to be left alone")
	}
}

// ONE LEVEL, NEVER MkdirAll. A typo in the part of the path that was supposed to
// already exist must stay an error — a typo that silently succeeds produces a
// destination that looks like it is working while the backups go somewhere
// nobody will think to look.
func TestAMistypedDestinationPathIsStillAnError(t *testing.T) {
	isolate(t)
	drive := t.TempDir()
	cfg := config.New()
	cfg.General.MachineName = "my-laptop"

	missing := filepath.Join(drive, "not-a-real-mount", "backups")
	if _, err := AppendDriveTargetIn(cfg, missing, "card", true, false); err == nil {
		t.Fatal("a path with a missing parent was accepted and the whole tree created")
	}
	if _, err := os.Stat(filepath.Join(drive, "not-a-real-mount")); !os.IsNotExist(err) {
		t.Error("a directory was created in a place the user did not name")
	}
	if len(cfg.Targets) != 0 {
		t.Error("the target was added despite the failure")
	}
}

// The name is rejected rather than rewritten. A folder that comes back called
// "my_backups" when the user asked for "my:backups" is a folder they will not
// recognise on the drive.
func TestAFolderNameWithASeparatorIsRefused(t *testing.T) {
	bad := map[string]string{
		"a slash":          "nested/backups",
		"a backslash":      `nested\backups`,
		"parent":           "..",
		"current":          ".",
		"hidden":           ".backups",
		"a colon":          "my:backups",
		"a wildcard":       "back*ups",
		"empty":            "",
		"a trailing space": "backups ",
	}
	for what, name := range bad {
		if err := ValidateFolderName(name); err == nil {
			t.Errorf("%s (%q) was accepted as a folder name", what, name)
		}
	}
	for _, good := range []string{"backups", "my backups", "laptop-2026", "Backups"} {
		if err := ValidateFolderName(good); err != nil {
			t.Errorf("%q is a perfectly ordinary folder name and was refused: %v", good, err)
		}
	}
}

// CLAUDE.md's standing rule, stated for the directory this change now creates:
// removing a destination is a change of intent, and never deletes a backup.
func TestARemovedDestinationLeavesItsFolderOnTheDrive(t *testing.T) {
	isolate(t)
	drive := t.TempDir()
	cfg := config.New()
	cfg.General.MachineName = "my-laptop"

	target := filepath.Join(drive, "backups")
	if _, err := AppendDriveTargetIn(cfg, target, "card", true, false); err != nil {
		t.Fatal(err)
	}
	// Something in it, so "still there" means something.
	mirrored := filepath.Join(target, "my-laptop", "Development", "notes.txt")
	if err := os.MkdirAll(filepath.Dir(mirrored), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(mirrored, []byte("the only copy"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := cfg.Save(); err != nil {
		t.Fatal(err)
	}

	if err := RemoveTarget("card"); err != nil {
		t.Fatalf("RemoveTarget: %v", err)
	}

	if _, err := os.Stat(mirrored); err != nil {
		t.Fatalf("REMOVING A DESTINATION DELETED THE BACKUP IN IT: %v", err)
	}
	after, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	for _, tg := range after.Targets {
		if tg.Name == "card" {
			t.Error("the target was not removed from the configuration")
		}
	}
}

// A folder destination must be findable by "Restore this machine", or somebody
// who chose one has backups no screen can see.
func TestAdoptFindsBackupsInAFolderOnADrive(t *testing.T) {
	isolate(t)
	drive := t.TempDir()
	cfg := config.New()
	cfg.General.MachineName = "my-laptop"

	target := filepath.Join(drive, "backups")
	if _, err := AppendDriveTargetIn(cfg, target, "card", true, false); err != nil {
		t.Fatal(err)
	}
	if err := WriteManifest(localmirror.NewLocalFS(target), cfg, nil, "install-laptop", "card"); err != nil {
		t.Fatal(err)
	}

	got := scanRoots([]string{drive})
	if len(got) != 1 {
		t.Fatalf("a scan of the drive found %d candidates, want 1: %+v", len(got), got)
	}
	if got[0].MachineName != "my-laptop" {
		t.Errorf("candidate names %q, want my-laptop", got[0].MachineName)
	}
	if got[0].Path != target {
		t.Errorf("candidate points at %q, want the folder %q", got[0].Path, target)
	}
}
