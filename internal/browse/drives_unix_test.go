// SPDX-License-Identifier: MIT

//go:build linux || darwin

package browse

import (
	"os"
	"path/filepath"
	"testing"
)

// fakeMountParent points the drive picker at a directory of our own, so a test
// can put things in it that the real /media and /mnt would not hold on demand.
func fakeMountParent(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	old := mountParentsFn
	mountParentsFn = func() []string { return []string{dir} }
	t.Cleanup(func() { mountParentsFn = old })
	return dir
}

// The guarantee: a mount point with nothing mounted on it is an ordinary
// folder on the system disk, and offering it as a drive would send backups to
// the very machine they exist to survive. This is the /mnt/backups case from
// the Raspberry Pi guide, where the directory is deliberately left in place for
// the drive to be mounted over.
func TestAFolderWithNothingMountedOnItIsNeverOfferedAsADrive(t *testing.T) {
	parent := fakeMountParent(t)
	waiting := filepath.Join(parent, "backups")
	if err := os.MkdirAll(waiting, 0o755); err != nil {
		t.Fatal(err)
	}

	for _, d := range Drives() {
		if d.Path == waiting {
			t.Fatalf("a folder with nothing mounted on it was offered as somewhere to put backups: %+v", d)
		}
	}
}

// Refusing it silently would be its own trap — the user is left looking for a
// drive that the program has decided not to mention. It has to say so.
func TestAFolderWithNothingMountedOnItIsExplained(t *testing.T) {
	parent := fakeMountParent(t)
	waiting := filepath.Join(parent, "backups")
	if err := os.MkdirAll(waiting, 0o755); err != nil {
		t.Fatal(err)
	}

	var found *Unusable
	for _, u := range unmountedParents() {
		if u.Path == waiting {
			found = &u
			break
		}
	}
	if found == nil {
		t.Fatal("the folder was dropped from the picker without a word about why")
	}
	if found.Reason != ReasonNotAMount {
		t.Errorf("reason = %q, want %q", found.Reason, ReasonNotAMount)
	}
	if found.Detail == "" {
		t.Error("no explanation offered")
	}
	if found.Confirm != "" {
		t.Error("a directory is not something prepare-drive can format; it must not offer a confirmation")
	}
}

// /media/<user> sits inside /media, so a naive scan finds it and warns that the
// user's own mount parent is an empty drive bay. Found on a real machine: it
// was the only thing the new warning had to say, which is exactly how people
// learn to ignore warnings.
func TestAMountParentIsNotReportedAsAnEmptyDrive(t *testing.T) {
	dir := t.TempDir()
	nested := filepath.Join(dir, "alex")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	old := mountParentsFn
	mountParentsFn = func() []string { return []string{nested, dir} }
	t.Cleanup(func() { mountParentsFn = old })

	for _, u := range unmountedParents() {
		if u.Path == nested {
			t.Fatalf("a mount parent was reported as an unmounted drive: %+v", u)
		}
	}
}

// A file sitting in a mount parent is not a drive and must not become one.
func TestOnlyDirectoriesAreConsidered(t *testing.T) {
	parent := fakeMountParent(t)
	if err := os.WriteFile(filepath.Join(parent, "notes.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	for _, u := range unmountedParents() {
		if filepath.Base(u.Path) == "notes.txt" {
			t.Fatalf("a plain file was reported as storage: %+v", u)
		}
	}
	for _, d := range Drives() {
		if filepath.Base(d.Path) == "notes.txt" {
			t.Fatalf("a plain file was offered as a drive: %+v", d)
		}
	}
}
