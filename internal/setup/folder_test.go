// SPDX-License-Identifier: MIT

package setup

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/phil9922/backup-maker/internal/config"
)

// addTestFolder protects a throwaway directory and returns its id.
func addTestFolder(t *testing.T, ignores []string, noDefaults bool) string {
	t.Helper()
	src := mustDir(t, t.TempDir(), "src")
	f, err := AddFolder(src, "src", ignores, noDefaults)
	if err != nil {
		t.Fatal(err)
	}
	return f.ID
}

func folderByID(t *testing.T, id string) config.Folder {
	t.Helper()
	for _, f := range load(t).Folders {
		if f.ID == id {
			return f
		}
	}
	t.Fatalf("folder %q vanished from the saved config", id)
	return config.Folder{}
}

// The list the user is shown is the list they get back: editing replaces,
// it never merges with what was there before.
func TestSetFolderIgnoresReplacesTheList(t *testing.T) {
	isolate(t)
	id := addTestFolder(t, []string{"old", "stale"}, false)

	if err := SetFolderIgnores(id, []string{"scratch", "*.iso"}, false); err != nil {
		t.Fatalf("SetFolderIgnores: %v", err)
	}
	got := folderByID(t, id).ExtraIgnore
	if len(got) != 2 || got[0] != "scratch" || got[1] != "*.iso" {
		t.Errorf("excludes = %v, want exactly the new list in order", got)
	}
}

// Patterns are typed by hand, in a text box: stray spaces and an accidental
// double entry must not become part of the pattern or clutter config.toml.
func TestSetFolderIgnoresTrimsAndDeduplicates(t *testing.T) {
	isolate(t)
	id := addTestFolder(t, nil, false)

	if err := SetFolderIgnores(id, []string{"  scratch ", "", "*.iso", "scratch", "   "}, false); err != nil {
		t.Fatalf("SetFolderIgnores: %v", err)
	}
	got := folderByID(t, id).ExtraIgnore
	if len(got) != 2 || got[0] != "scratch" || got[1] != "*.iso" {
		t.Errorf("excludes = %v, want [scratch *.iso] — trimmed, de-duplicated, order kept", got)
	}
}

// Clearing the box must leave the key out of config.toml entirely, rather than
// writing an empty list that reads as a deliberate setting.
func TestSetFolderIgnoresClearsToNothing(t *testing.T) {
	isolate(t)
	id := addTestFolder(t, []string{"scratch"}, false)

	if err := SetFolderIgnores(id, []string{"  ", ""}, false); err != nil {
		t.Fatalf("SetFolderIgnores: %v", err)
	}
	if got := folderByID(t, id).ExtraIgnore; got != nil {
		t.Errorf("excludes = %v, want nil", got)
	}
	path, err := config.ConfigPath()
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "extra_ignore") {
		t.Errorf("config.toml still carries an empty extra_ignore:\n%s", data)
	}
}

// A folder that opted out of the standard exclusions must stay opted out
// across an edit — the flag is saved, not inferred.
func TestSetFolderIgnoresPersistsNoDefaultIgnores(t *testing.T) {
	isolate(t)
	id := addTestFolder(t, nil, false)

	if err := SetFolderIgnores(id, []string{"scratch"}, true); err != nil {
		t.Fatalf("SetFolderIgnores: %v", err)
	}
	if !folderByID(t, id).NoDefaultIgnores {
		t.Error("no_default_ignores was not saved")
	}
	if err := SetFolderIgnores(id, []string{"scratch"}, false); err != nil {
		t.Fatalf("SetFolderIgnores: %v", err)
	}
	if folderByID(t, id).NoDefaultIgnores {
		t.Error("no_default_ignores could not be turned back off")
	}
}

// A stale dashboard tab (or a folder removed in another window) must get a
// clear refusal, not a silent no-op.
func TestSetFolderIgnoresRejectsUnknownFolder(t *testing.T) {
	isolate(t)
	addTestFolder(t, nil, false)

	err := SetFolderIgnores("no-such-id", []string{"scratch"}, false)
	if err == nil {
		t.Fatal("editing an unknown folder was accepted")
	}
	if !strings.Contains(err.Error(), "no-such-id") {
		t.Errorf("error %q should name the folder that wasn't found", err)
	}
}

// THE COLLISION THIS CLOSES, and it needed no ill intent to reach: a folder's
// default label is the last element of its path, so two directories with the
// same basename in different parents both defaulted to it — and then mirrored
// into one destination directory, each pass versioning away whatever the other
// had just written.
func TestTwoFoldersWithTheSameBasenameDoNotShareADestination(t *testing.T) {
	isolate(t)
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	cfg.General.MachineName = "my-laptop"
	work := t.TempDir()
	personal := t.TempDir()
	mustDir(t, work, "src")
	mustDir(t, personal, "src")

	a, err := AppendFolder(cfg, filepath.Join(work, "src"), "", nil, false)
	if err != nil {
		t.Fatal(err)
	}
	b, err := AppendFolder(cfg, filepath.Join(personal, "src"), "", nil, false)
	if err != nil {
		t.Fatal(err)
	}

	if a.Label == b.Label {
		t.Fatalf("both folders took the label %q; they would back up into one directory and delete each other's files", a.Label)
	}
	if da, db := config.DestRoot("my-laptop", a.Label), config.DestRoot("my-laptop", b.Label); da == db {
		t.Errorf("different labels still resolve to the same destination %q", da)
	}
	if err := cfg.Validate(); err != nil {
		t.Errorf("the config the setup path produced does not validate: %v", err)
	}
}

// A label somebody TYPED is refused rather than quietly altered: backing their
// work up under a name they did not ask for is worse than an error.
func TestATypedLabelThatIsAlreadyInUseIsRefused(t *testing.T) {
	isolate(t)
	cfg, _ := config.Load()
	cfg.General.MachineName = "my-laptop"
	work := t.TempDir()
	personal := t.TempDir()
	mustDir(t, work, "src")
	mustDir(t, personal, "src")

	if _, err := AppendFolder(cfg, filepath.Join(work, "src"), "code", nil, false); err != nil {
		t.Fatal(err)
	}
	_, err := AppendFolder(cfg, filepath.Join(personal, "src"), "code", nil, false)
	if err == nil {
		t.Fatal("a duplicate typed label was accepted")
	}
	if len(cfg.Folders) != 1 {
		t.Errorf("the refused folder was added anyway: %d folders", len(cfg.Folders))
	}
}

// Different labels that SANITIZE to the same directory are the same collision
// wearing a disguise, so the check compares destinations rather than labels.
func TestLabelsThatSanitizeToTheSameDirectoryCollide(t *testing.T) {
	isolate(t)
	cfg, _ := config.Load()
	cfg.General.MachineName = "my-laptop"
	work := t.TempDir()
	personal := t.TempDir()
	mustDir(t, work, "src")
	mustDir(t, personal, "src")

	if _, err := AppendFolder(cfg, filepath.Join(work, "src"), "a/b", nil, false); err != nil {
		t.Fatal(err)
	}
	if _, err := AppendFolder(cfg, filepath.Join(personal, "src"), "a_b", nil, false); err == nil {
		t.Error(`"a/b" and "a_b" are different labels that land in the same directory, and both were accepted`)
	}
}

// A new folder must not be given a stopped folder's destination: the mirror
// would adopt that copy and version away everything the new source does not
// have — deleting a backup somebody has not decided about yet.
func TestANewFolderDoesNotTakeAStoppedFoldersDestination(t *testing.T) {
	isolate(t)
	cfg, _ := config.Load()
	cfg.General.MachineName = "my-laptop"
	cfg.Retired = []config.Retired{{
		ID: "old", Path: "/home/pk/Development", Label: "development",
	}}
	elsewhere := t.TempDir()
	mustDir(t, elsewhere, "development")

	if _, err := AppendFolder(cfg, filepath.Join(elsewhere, "development"), "development", nil, false); err == nil {
		t.Error("a new folder was allowed to take a stopped backup's destination")
	}
}

// But adding back the SAME directory is not a collision — that is a re-add,
// and the mirror reconciles against the copy already there.
func TestAddingBackTheSameDirectoryIsAllowed(t *testing.T) {
	isolate(t)
	cfg, _ := config.Load()
	cfg.General.MachineName = "my-laptop"
	dir := t.TempDir()
	src := filepath.Join(dir, "development")
	mustDir(t, dir, "development")
	cfg.Retired = []config.Retired{{ID: "old", Path: src, Label: "development"}}

	f, err := AppendFolder(cfg, src, "development", nil, false)
	if err != nil {
		t.Fatalf("adding back the folder that was stopped was refused: %v", err)
	}
	if f.Label != "development" {
		t.Errorf("label was changed to %q; it must match so the existing copy is reconciled rather than re-copied", f.Label)
	}
}

// THE HOLE THE FIRST FIX LEFT, found by running the real thing against a real
// SD card. Backup destinations are mostly exFAT (cards, sticks), NTFS (Windows
// shares) or SMB, and all of them treat "Development" and "development" as one
// directory — so a case-only difference passed a case-sensitive check and the
// two folders then shared a directory on exactly the storage people back up to
// most.
func TestLabelsDifferingOnlyInCaseCollide(t *testing.T) {
	isolate(t)
	cfg, _ := config.Load()
	cfg.General.MachineName = "my-laptop"
	work := t.TempDir()
	personal := t.TempDir()
	mustDir(t, work, "src")
	mustDir(t, personal, "src")

	if _, err := AppendFolder(cfg, filepath.Join(work, "src"), "Development", nil, false); err != nil {
		t.Fatal(err)
	}
	if _, err := AppendFolder(cfg, filepath.Join(personal, "src"), "development", nil, false); err == nil {
		t.Error(`"Development" and "development" are one directory on exFAT, NTFS and SMB, and both were accepted`)
	}
}

// And the defaulted label disambiguates past a case-only clash too, rather than
// handing back a name that only looks distinct.
func TestADefaultedLabelSkipsPastACaseOnlyClash(t *testing.T) {
	isolate(t)
	cfg, _ := config.Load()
	cfg.General.MachineName = "my-laptop"
	work := t.TempDir()
	mustDir(t, work, "Src")
	personal := t.TempDir()
	mustDir(t, personal, "src")

	a, err := AppendFolder(cfg, filepath.Join(work, "Src"), "", nil, false)
	if err != nil {
		t.Fatal(err)
	}
	b, err := AppendFolder(cfg, filepath.Join(personal, "src"), "", nil, false)
	if err != nil {
		t.Fatal(err)
	}
	if config.SameDest(config.DestRoot("my-laptop", a.Label), config.DestRoot("my-laptop", b.Label)) {
		t.Errorf("labels %q and %q still land in the same directory on a case-insensitive filesystem", a.Label, b.Label)
	}
	if err := cfg.Validate(); err != nil {
		t.Errorf("the config the setup path produced does not validate: %v", err)
	}
}
