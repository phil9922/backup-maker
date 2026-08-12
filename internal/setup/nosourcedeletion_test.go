// SPDX-License-Identifier: MIT

package setup

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/phil9922/backup-maker/internal/archive"
	"github.com/phil9922/backup-maker/internal/config"
	"github.com/phil9922/backup-maker/internal/localmirror"
)

// THE RULE THIS FILE EXISTS FOR: backup-maker never deletes files from a
// source folder. No exception, no cleanup case, no "unless asked". See
// CLAUDE.md at the repo root.
//
// The reason it outranks every other guarantee here: this program exists so
// that a machine failing does not cost somebody their files. A backup tool
// that can delete the original is worse than no tool at all, because the user
// has been told their files are protected and will not have kept a second
// copy. The failure is unrecoverable and silent until it matters most.
//
// Every path in this package that can delete anything gets a case here. If a
// new one is added, add its case too.
func TestNothingInThisPackageEverDeletesFromASource(t *testing.T) {
	isolate(t)

	// A source with a file in it, and a destination holding its backup.
	base := t.TempDir()
	src := mustDir(t, base, "src")
	canary := filepath.Join(src, "irreplaceable.txt")
	if err := os.WriteFile(canary, []byte("the only copy"), 0o644); err != nil {
		t.Fatal(err)
	}
	dest := mustDir(t, base, "dest")
	// The machine name has to be the config's own, or the paths written below
	// would not be the paths the delete aims at — and the test would pass
	// while deleting nothing.
	machine := load(t).General.MachineName
	if err := EnsureTargetMarker(localmirror.NewLocalFS(dest), "card", machine); err != nil {
		t.Fatal(err)
	}

	folder, targets, err := CreateBackup(BackupRequest{Path: src, Destinations: []Destination{{Path: dest}}})
	if err != nil {
		t.Fatal(err)
	}
	if err := AddArchive("nightly", []string{folder.ID}, "daily", targets[0].Name, 3, "pw"); err != nil {
		t.Fatal(err)
	}
	// Something for the delete paths to actually find and remove.
	writeUnder(t, dest, config.DestRoot(machine, folder.Label)+"/copy.txt", "a backup")
	writeUnder(t, dest, config.VersionRoot(machine, folder.Label)+"/copy~20260101-000000.txt", "an old backup")
	writeUnder(t, dest, archive.PathFor(machine, "nightly")+"/nightly-20260101-020000.zip", "a snapshot")

	// Both deleting paths open the destination through this seam, so both are
	// aimed at the temp directory above and never at anything else.
	open := func(config.Target) (localmirror.Backend, error) { return localmirror.NewLocalFS(dest), nil }

	stillThere := func(t *testing.T, after string) {
		t.Helper()
		b, err := os.ReadFile(canary)
		if err != nil {
			t.Fatalf("THE SOURCE FILE WAS DELETED by %s: %v", after, err)
		}
		if string(b) != "the only copy" {
			t.Fatalf("the source file was modified by %s", after)
		}
		if _, err := os.Stat(src); err != nil {
			t.Fatalf("THE SOURCE FOLDER WAS DELETED by %s: %v", after, err)
		}
	}

	// Every mutating path in this package, in the order a user could reach
	// them — each one ending with the source intact.
	if err := SetFolderIgnores(folder.ID, []string{"*.log"}, false); err != nil {
		t.Fatal(err)
	}
	stillThere(t, "SetFolderIgnores")

	if err := SetArchiveSchedule("nightly", "weekly", 2, nil); err != nil {
		t.Fatal(err)
	}
	stillThere(t, "SetArchiveSchedule")

	if err := SetArchivePaused("nightly", true); err != nil {
		t.Fatal(err)
	}
	stillThere(t, "SetArchivePaused")

	if err := RemoveArchive("nightly"); err != nil {
		t.Fatal(err)
	}
	stillThere(t, "RemoveArchive")

	// The file view's delete — the second path in this package that hands a
	// path to RemoveAll on a destination. The schedule is gone, so its zips
	// belong to nothing and this removes them.
	if _, err := DeleteDestFile(targets[0].Name, archive.PathFor(machine, "nightly"), "nightly", open); err != nil {
		t.Fatal(err)
	}
	stillThere(t, "DeleteDestFile")
	if exists(t, dest, archive.PathFor(machine, "nightly")) {
		t.Error("the orphaned snapshots were not deleted, so this proves nothing about the source surviving a real deletion")
	}

	if err := RemoveFolder(folder.ID); err != nil {
		t.Fatal(err)
	}
	stillThere(t, "RemoveFolder")

	// The other action that deletes backups on purpose — the sharpest test of
	// the rule, since it really does call RemoveAll.
	if _, err := DeleteRetiredBackups(folder.ID, folder.Label, open); err != nil {
		t.Fatal(err)
	}
	stillThere(t, "DeleteRetiredBackups")

	// And the backup really was removed, so the test above is not passing
	// merely because nothing happened.
	if _, err := os.Stat(filepath.Join(dest, config.DestRoot(machine, folder.Label))); err == nil {
		t.Error("the backup was not deleted, so this test proves nothing about the source surviving a real deletion")
	}

	if err := RemoveTarget(targets[0].Name); err != nil {
		t.Fatal(err)
	}
	stillThere(t, "RemoveTarget")
}
