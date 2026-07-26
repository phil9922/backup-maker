// SPDX-License-Identifier: MIT

package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// pointConfigDirInto makes the configuration directory land inside base and
// creates it, the way a real machine has it inside the user's home.
func pointConfigDirInto(t *testing.T, base string) string {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(base, ".config")) // linux
	t.Setenv("HOME", base)                                      // macOS
	dir, err := Dir()
	if err != nil {
		t.Fatalf("Dir: %v", err)
	}
	return dir
}

func relTo(t *testing.T, root, target string) string {
	t.Helper()
	rel, err := filepath.Rel(root, target)
	if err != nil {
		t.Fatalf("Rel(%q, %q): %v", root, target, err)
	}
	return filepath.ToSlash(rel)
}

func contains(list []string, want string) bool {
	for _, s := range list {
		if s == want {
			return true
		}
	}
	return false
}

// The folder being backed up holds our configuration directory: it must be
// reported, as a path relative to that folder.
func TestSelfExcludesFindsTheConfigDirInsideAFolder(t *testing.T) {
	home := t.TempDir()
	dir := pointConfigDirInto(t, home)

	got := SelfExcludes(home)
	want := relTo(t, home, dir)
	if !contains(got, want) {
		t.Fatalf("SelfExcludes(%q) = %v, want it to contain %q", home, got, want)
	}
	for _, rel := range got {
		if rel == "" || strings.HasPrefix(rel, "..") {
			t.Errorf("SelfExcludes returned %q, which does not point inside the folder", rel)
		}
	}
}

// The trap: a directory that merely carries the name. Excluding it would drop
// this project's own source tree out of its author's backups.
func TestSelfExcludesLeavesADirectoryMerelyNamedBackupMakerAlone(t *testing.T) {
	home := t.TempDir()
	pointConfigDirInto(t, t.TempDir()) // the real one lives somewhere else

	decoy := filepath.Join(home, "Development", "backup-maker")
	if err := os.MkdirAll(decoy, 0o755); err != nil {
		t.Fatal(err)
	}
	if got := SelfExcludes(home); len(got) != 0 {
		t.Fatalf("SelfExcludes(%q) = %v, want nothing: %q is the user's own directory", home, got, decoy)
	}
}

// Nested well below the folder root, which is where a home directory keeps it.
func TestSelfExcludesFindsADeeplyNestedConfigDir(t *testing.T) {
	root := t.TempDir()
	deep := filepath.Join(root, "users", "pk", "profile")
	if err := os.MkdirAll(deep, 0o755); err != nil {
		t.Fatal(err)
	}
	dir := pointConfigDirInto(t, deep)

	want := relTo(t, root, dir)
	if got := SelfExcludes(root); !contains(got, want) {
		t.Fatalf("SelfExcludes(%q) = %v, want it to contain %q", root, got, want)
	}
}

// A folder that does not contain the configuration directory gets no exclusion
// at all — the rule must be invisible to everyone else.
func TestSelfExcludesDoesNothingForAnUnrelatedFolder(t *testing.T) {
	pointConfigDirInto(t, t.TempDir())
	if got := SelfExcludes(t.TempDir()); len(got) != 0 {
		t.Fatalf("SelfExcludes = %v, want nothing for a folder the config dir is outside of", got)
	}
}

// A sibling whose name starts with the folder's own is outside it. A plain
// string prefix gets this wrong, and getting it wrong here would exclude part
// of a folder that has nothing to do with us.
func TestSelfExcludesStopsAtASeparator(t *testing.T) {
	base := t.TempDir()
	home := filepath.Join(base, "home")
	sibling := filepath.Join(base, "home-old")
	if err := os.MkdirAll(home, 0o755); err != nil {
		t.Fatal(err)
	}
	pointConfigDirInto(t, sibling)

	if got := SelfExcludes(home); len(got) != 0 {
		t.Fatalf("SelfExcludes(%q) = %v, want nothing: the config dir is in %q", home, got, sibling)
	}
}

// Backing up the configuration directory itself (or a piece of it) excludes the
// whole folder: an empty string, meaning "none of this may be copied".
func TestSelfExcludesCoversTheWholeFolderWhenItIsTheConfigDir(t *testing.T) {
	dir := pointConfigDirInto(t, t.TempDir())

	for _, root := range []string{dir, filepath.Join(dir, "logs")} {
		got := SelfExcludes(root)
		if len(got) != 1 || got[0] != "" {
			t.Errorf("SelfExcludes(%q) = %v, want [\"\"] (the entire folder)", root, got)
		}
	}
}
