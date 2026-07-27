// SPDX-License-Identifier: MIT

package browse

import (
	"os"
	"path/filepath"
	"strconv"
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
