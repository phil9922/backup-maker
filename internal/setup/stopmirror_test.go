// SPDX-License-Identifier: MIT

package setup

import (
	"testing"

	"github.com/phil9922/backup-maker/internal/config"
)

// THE GUARANTEE: stopping the backup you did not want never stops the one you
// did.
//
// This is the second half of what went wrong on a real machine. A timed
// snapshot was asked for; an unwanted continuous mirror appeared alongside it;
// the only button offered was Stop, which retires the whole folder — so
// pressing it silently took away the daily snapshot as well, leaving the
// folder backed up by nothing while the schedule sat there saying "never run".
func TestStoppingAMirrorLeavesAScheduleStillRunning(t *testing.T) {
	isolate(t)
	base := t.TempDir()
	src := mustDir(t, base, "desktop")
	dest := mustDir(t, base, "card")

	folder, targets, err := CreateBackup(BackupRequest{Path: src, Destinations: []Destination{{Path: dest}}})
	if err != nil {
		t.Fatal(err)
	}
	if err := AddArchive("nightly", []string{folder.ID}, "daily", targets[0].Name, 3, "pw"); err != nil {
		t.Fatal(err)
	}

	if err := StopMirroring(folder.ID); err != nil {
		t.Fatal(err)
	}

	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Mirrored(folder.ID) {
		t.Error("the folder is still being mirrored after its mirror was stopped")
	}
	if !cfg.Snapshotted(folder.ID) {
		t.Fatal("stopping the mirror also stopped the snapshot schedule — the backup that was actually wanted")
	}
	// The folder is still protected, so it must NOT have been retired: the
	// "No longer protected" list is for folders with no backup at all.
	for _, r := range cfg.Retired {
		if r.ID == folder.ID {
			t.Error("the folder was retired even though its snapshot schedule still covers it")
		}
	}
	if len(cfg.Folders) != 1 || cfg.Folders[0].ID != folder.ID {
		t.Error("the folder was removed from the configuration")
	}
}

// A destination told to mirror exactly this folder must end up mirroring
// nothing — and must never end up with an EMPTY folder list, which would mean
// "every folder" and silently widen it to everything on the machine.
func TestStoppingAMirrorNeverWidensADestinationToEveryFolder(t *testing.T) {
	isolate(t)
	base := t.TempDir()
	src := mustDir(t, base, "desktop")
	other := mustDir(t, base, "development")
	dest := mustDir(t, base, "card")

	folder, targets, err := CreateBackup(BackupRequest{Path: src, Destinations: []Destination{{Path: dest}}})
	if err != nil {
		t.Fatal(err)
	}
	// Read the scope back from the saved config: the target returned by
	// CreateBackup is a copy taken before it is scoped, so its Folders list is
	// empty and asserting on it proves nothing.
	if scoped := loadTarget(t, targets[0].Name); len(scoped.Folders) != 1 {
		t.Fatalf("this test needs a destination scoped to one folder, got %v", scoped.Folders)
	}
	// A second folder that this destination must never start mirroring.
	second, _, err := CreateBackup(BackupRequest{
		Path: other, Destinations: []Destination{{Path: mustDir(t, base, "card2")}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := AddArchive("nightly", []string{folder.ID}, "daily", targets[0].Name, 3, "pw"); err != nil {
		t.Fatal(err)
	}

	if err := StopMirroring(folder.ID); err != nil {
		t.Fatal(err)
	}

	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	for _, target := range cfg.Targets {
		for _, f := range cfg.FoldersForTarget(target) {
			if f.ID == folder.ID {
				t.Errorf("%q still mirrors the folder whose mirror was stopped", target.Name)
			}
			if target.Name == targets[0].Name && f.ID == second.ID {
				t.Errorf("%q was widened to a folder it was never told to mirror", target.Name)
			}
		}
	}
}

// Stopping the mirror of a folder that has no schedule would leave it backed
// up by nothing while still listed as protected. That is the silent
// no-backup state, so it is refused and the caller is told what to do instead.
func TestStoppingTheOnlyBackupOfAFolderIsRefused(t *testing.T) {
	isolate(t)
	base := t.TempDir()
	src := mustDir(t, base, "src")
	dest := mustDir(t, base, "dest")

	folder, _, err := CreateBackup(BackupRequest{Path: src, Destinations: []Destination{{Path: dest}}})
	if err != nil {
		t.Fatal(err)
	}

	err = StopMirroring(folder.ID)
	if err == nil {
		t.Fatal("stopping the only backup of a folder was allowed, leaving it protected by nothing")
	}
	cfg, _ := config.Load()
	if !cfg.Mirrored(folder.ID) {
		t.Error("the mirror was stopped anyway")
	}
}

// THE GUARANTEE: tidying up a stopped folder never hands a destination folders
// it was never told to mirror.
//
// An empty folder list means EVERY folder. A destination scoped to exactly one
// folder therefore becomes a destination for everything the moment that folder
// is dropped from its list — silently, at the moment somebody clears up a
// folder they stopped. Found by testing the clean-up path behind "Forget this"
// and "Delete backups…", both of which run dropFolderRefs.
func TestForgettingAStoppedFolderNeverWidensADestinationToEveryFolder(t *testing.T) {
	isolate(t)
	base := t.TempDir()
	one := mustDir(t, base, "desktop")
	two := mustDir(t, base, "development")

	stopped, targets, err := CreateBackup(BackupRequest{
		Path: one, Destinations: []Destination{{Path: mustDir(t, base, "card")}},
	})
	if err != nil {
		t.Fatal(err)
	}
	scoped := loadTarget(t, targets[0].Name)
	if len(scoped.Folders) != 1 {
		t.Fatalf("this test needs a destination scoped to one folder, got %v", scoped.Folders)
	}
	// A folder that destination must never start mirroring.
	other, _, err := CreateBackup(BackupRequest{
		Path: two, Destinations: []Destination{{Path: mustDir(t, base, "card2")}},
	})
	if err != nil {
		t.Fatal(err)
	}

	if err := RemoveFolder(stopped.ID); err != nil {
		t.Fatal(err)
	}
	if err := ForgetRetired(stopped.ID); err != nil {
		t.Fatal(err)
	}

	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	for _, target := range cfg.Targets {
		if target.Name != scoped.Name {
			continue
		}
		for _, f := range cfg.FoldersForTarget(target) {
			if f.ID == other.ID {
				t.Fatalf("%q was widened to mirror %q, a folder it was never told to mirror", target.Name, f.Label)
			}
		}
	}
}
