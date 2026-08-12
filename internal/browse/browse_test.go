// SPDX-License-Identifier: MIT

package browse

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestDirsRejectsRelativePaths(t *testing.T) {
	if _, err := Dirs("relative/path"); err == nil {
		t.Error("a relative path was accepted; the API contract is absolute-only")
	}
}

// The picker must never become a file browser: leaking file names would expose
// more than the folder chooser needs.
func TestDirsReturnsDirectoriesOnly(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "keepme"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"secret.txt", "passwords.csv"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	got, err := Dirs(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Entries) != 1 || got.Entries[0].Name != "keepme" {
		t.Fatalf("expected only the directory, got %+v", got.Entries)
	}
}

func TestDirsReportsParent(t *testing.T) {
	root := t.TempDir()
	child := filepath.Join(root, "child")
	if err := os.Mkdir(child, 0o755); err != nil {
		t.Fatal(err)
	}
	got, err := Dirs(child)
	if err != nil {
		t.Fatal(err)
	}
	if got.Parent != root {
		t.Errorf("Parent = %q, want %q", got.Parent, root)
	}
}

// A symlink pointing outside the directory being listed must not silently
// become a doorway to somewhere else.
func TestDirsSkipsEscapingSymlinks(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(root, "escape")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	inside := filepath.Join(root, "real")
	if err := os.Mkdir(inside, 0o755); err != nil {
		t.Fatal(err)
	}

	got, err := Dirs(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range got.Entries {
		if e.Name == "escape" {
			t.Error("a symlink leading outside the listed directory was included")
		}
	}
	if len(got.Entries) != 1 || got.Entries[0].Name != "real" {
		t.Errorf("expected just the real directory, got %+v", got.Entries)
	}
}

func TestDirsCapsEntries(t *testing.T) {
	root := t.TempDir()
	for i := 0; i < MaxEntries+25; i++ {
		if err := os.Mkdir(filepath.Join(root, "d"+strconv.Itoa(i)), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	got, err := Dirs(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Entries) != MaxEntries {
		t.Errorf("returned %d entries, want the %d cap", len(got.Entries), MaxEntries)
	}
	if !got.Truncated {
		t.Error("Truncated should be set so the UI can say so out loud")
	}
}

// One unreadable subdirectory must not make the whole picker useless.
func TestDirsToleratesUnreadableChildren(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root; permission bits are not enforced")
	}
	root := t.TempDir()
	locked := filepath.Join(root, "locked")
	if err := os.Mkdir(locked, 0o000); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(locked, 0o755)
	if err := os.Mkdir(filepath.Join(root, "open"), 0o755); err != nil {
		t.Fatal(err)
	}

	got, err := Dirs(root)
	if err != nil {
		t.Fatalf("listing failed because of one unreadable child: %v", err)
	}
	if len(got.Entries) < 1 {
		t.Error("readable siblings should still be listed")
	}
}

func TestDirsRejectsFiles(t *testing.T) {
	root := t.TempDir()
	f := filepath.Join(root, "a.txt")
	if err := os.WriteFile(f, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Dirs(f); err == nil {
		t.Error("a file path was accepted as a directory listing")
	}
}

func TestRootsStartAtHome(t *testing.T) {
	roots := Roots()
	if len(roots) == 0 {
		t.Skip("no home directory in this environment")
	}
	if roots[0].Name != "Home" {
		t.Errorf("first root = %q, want Home so the picker opens somewhere familiar", roots[0].Name)
	}
}

// THE GUARANTEE: the picker never offers a folder beside the folder that
// contains it.
//
// Roots used to list Documents, Desktop, Pictures, Music and Videos next to
// Home. Once the wizard could choose several folders that stopped being a
// shortcut and became a trap: picking Home and Documents means copying those
// files twice to every destination and packing two copies into every snapshot.
func TestNoRootContainsAnother(t *testing.T) {
	roots := Roots()
	if len(roots) == 0 {
		t.Skip("no home directory in this environment")
	}
	for i, a := range roots {
		for j, b := range roots {
			if i == j {
				continue
			}
			rel, err := filepath.Rel(a.Path, b.Path)
			if err != nil {
				continue
			}
			if rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && rel != "." {
				t.Errorf("%q (%s) is inside %q (%s): the picker would offer a folder "+
					"beside its own parent, and choosing both backs those files up twice",
					b.Name, b.Path, a.Name, a.Path)
			}
		}
	}
}

// And the rule itself, rather than today's list: an entry inside another is
// dropped, so adding a mount point here later cannot recreate the trap.
func TestWithoutNestedDropsAFolderInsideAnother(t *testing.T) {
	got := withoutNested([]Entry{
		{Name: "Home", Path: filepath.Join("/home", "alex")},
		{Name: "Documents", Path: filepath.Join("/home", "alex", "Documents")},
		{Name: "Card", Path: filepath.Join("/media", "alex", "SDCARD")},
	})
	var names []string
	for _, e := range got {
		names = append(names, e.Name)
	}
	if len(names) != 2 || names[0] != "Home" || names[1] != "Card" {
		t.Errorf("kept %v, want Home and Card: a folder inside home must go, "+
			"and a separate drive must stay", names)
	}
}

// THE GUARANTEE: hidden folders come last, and they still come.
//
// A dot sorts before every letter, so a home directory answered with twenty
// dot-directories before Desktop. Sorting them last fixes that; hiding them
// would not, because ~/.ssh and ~/.config are things people deliberately back
// up, and a picker that omits them lets somebody set up a backup they believe
// is complete and is not.
func TestHiddenFoldersSortLastButAreStillListed(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{".cache", ".config", "Desktop", "Documents", ".ssh", "apps"} {
		if err := os.Mkdir(filepath.Join(dir, name), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	got, err := Dirs(dir)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range got.Entries {
		names = append(names, e.Name)
	}
	want := []string{"apps", "Desktop", "Documents", ".cache", ".config", ".ssh"}
	if strings.Join(names, ",") != strings.Join(want, ",") {
		t.Errorf("listing order = %v, want %v", names, want)
	}
	for _, hidden := range []string{".cache", ".config", ".ssh"} {
		if !strings.Contains(strings.Join(names, ","), hidden) {
			t.Errorf("%s was not listed at all — a folder somebody may want to back up is unreachable", hidden)
		}
	}
}
