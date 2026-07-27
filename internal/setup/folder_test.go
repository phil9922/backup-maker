// SPDX-License-Identifier: MIT

package setup

import (
	"os"
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
