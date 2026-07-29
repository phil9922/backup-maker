// SPDX-License-Identifier: MIT

package config

import (
	"reflect"
	"testing"
)

// liveConfig is the state a real machine was found in on 2026-07-28, verbatim:
// two folders, the second set up for timed snapshots only, its last schedule
// already deleted, and two destinations that both mirror "every folder".
func liveConfig() *Config {
	return &Config{
		Folders: []Folder{
			{ID: "f1", Label: "Development", Path: "/home/pk/Desktop/Development"},
			{ID: "f2", Label: "Desktop", Path: "/home/pk/Desktop", SnapshotOnly: true},
		},
		Targets: []Target{
			{Name: "laptopcard", Type: "drive"}, // no Folders → every folder
			{Name: "backup-pi", Type: "share"},  // no Folders → every folder
		},
		Archives: nil, // the schedule was deleted
	}
}

// THE GUARANTEE: a folder that no destination mirrors and no schedule
// snapshots is reported as protected by nothing.
//
// This is the state that had no name. Marking a folder SnapshotOnly keeps it
// out of every unscoped destination — deliberately, so that asking for one
// daily zip does not start a continuous mirror. Deleting its last schedule
// leaves the folder recorded — also deliberately, because deleting a schedule
// must never delete anything else. Both behaviours are right, and together they
// produce a folder that nothing at all copies, which the program went on
// describing as one "you're already protecting".
func TestASnapshotOnlyFolderWhoseScheduleWasDeletedIsProtectedByNothing(t *testing.T) {
	cfg := liveConfig()

	if cfg.Protected("f2") {
		t.Error("Desktop reports as protected, but no target mirrors it and no schedule snapshots it")
	}
	if cfg.Mirrored("f2") {
		t.Error("Desktop reports as mirrored; SnapshotOnly is supposed to keep it out of every unscoped target")
	}
	if cfg.Snapshotted("f2") {
		t.Error("Desktop reports as snapshotted, but its last schedule was deleted")
	}
	if !cfg.Protected("f1") {
		t.Error("Development reports as unprotected, but both targets mirror every folder")
	}

	bare := cfg.UnprotectedFolders()
	if len(bare) != 1 || bare[0].ID != "f2" {
		t.Errorf("UnprotectedFolders() = %v, want only Desktop", ids(bare))
	}
}

// A folder with no mirror but a live schedule is protected: one kind of backup
// is still a backup, and calling it unprotected would send somebody to fix a
// folder that is fine.
func TestAFolderWithOnlyASnapshotIsStillProtected(t *testing.T) {
	cfg := liveConfig()
	cfg.Archives = []Archive{{Name: "desktop-daily", Folders: []string{"f2"}}}

	if !cfg.Protected("f2") {
		t.Error("Desktop reports as unprotected while a schedule seals it into a zip")
	}
	if len(cfg.UnprotectedFolders()) != 0 {
		t.Errorf("UnprotectedFolders() = %v, want none", ids(cfg.UnprotectedFolders()))
	}
}

// A destination scoped to this folder explicitly protects it, even though
// SnapshotOnly keeps it out of the unscoped ones. Naming a folder is an
// instruction and outranks the flag — the same rule FoldersForTarget applies.
func TestASnapshotOnlyFolderNamedByADestinationIsProtected(t *testing.T) {
	cfg := liveConfig()
	cfg.Targets = append(cfg.Targets, Target{Name: "usb", Type: "drive", Folders: []string{"f2"}})

	if !cfg.Protected("f2") {
		t.Error("Desktop reports as unprotected while a destination names it explicitly")
	}
}

// An ArchivesOnly destination mirrors nothing, so it cannot be what protects a
// folder. Without this, a snapshot-only destination would make every folder
// look mirrored and hide the state this whole predicate exists to surface.
func TestAnArchivesOnlyDestinationDoesNotMakeAFolderMirrored(t *testing.T) {
	cfg := &Config{
		Folders: []Folder{{ID: "f1", Label: "Development"}},
		Targets: []Target{{Name: "backup-pi", Type: "share", ArchivesOnly: true}},
	}

	if cfg.Mirrored("f1") {
		t.Error("Development reports as mirrored by a destination that mirrors nothing")
	}
	if cfg.Protected("f1") {
		t.Error("Development reports as protected with no mirror and no schedule")
	}
}

// THE GUARANTEE: asking whether a folder is protected never changes anything.
//
// The tempting "fix" for Desktop is to clear SnapshotOnly so the unscoped
// destinations pick it up again. That is the 2026-07-28 incident in reverse: it
// silently starts a continuous mirror of a folder onto every drive configured,
// which is the exact failure SnapshotOnly was added to prevent. Reading the
// state must stay a read.
func TestReadingProtectionNeverChangesTheConfig(t *testing.T) {
	cfg := liveConfig()
	before := liveConfig()

	cfg.Protected("f1")
	cfg.Protected("f2")
	cfg.Mirrored("f2")
	cfg.Snapshotted("f2")
	cfg.UnprotectedFolders()

	if !reflect.DeepEqual(cfg, before) {
		t.Errorf("reading protection changed the config:\n got %+v\nwant %+v", cfg, before)
	}
	if !cfg.Folders[1].SnapshotOnly {
		t.Error("SnapshotOnly was cleared — that hands the folder to every unscoped destination")
	}
}

// Protected must not grow a rule of its own: it has to agree with the two
// functions that decide what actually gets copied, across every combination of
// the flags that make them disagree.
func TestProtectedAgreesWithFoldersForTargetAndFoldersForArchive(t *testing.T) {
	for _, snapshotOnly := range []bool{false, true} {
		for _, archivesOnly := range []bool{false, true} {
			for _, scoped := range []bool{false, true} {
				for _, hasArchive := range []bool{false, true} {
					cfg := &Config{
						Folders: []Folder{{ID: "f1", Label: "Development", SnapshotOnly: snapshotOnly}},
						Targets: []Target{{Name: "t", Type: "drive", ArchivesOnly: archivesOnly}},
					}
					if scoped {
						cfg.Targets[0].Folders = []string{"f1"}
					}
					if hasArchive {
						cfg.Archives = []Archive{{Name: "a"}}
					}

					want := false
					for _, t2 := range cfg.Targets {
						for _, f := range cfg.FoldersForTarget(t2) {
							if f.ID == "f1" {
								want = true
							}
						}
					}
					for _, a := range cfg.Archives {
						for _, f := range cfg.FoldersForArchive(a) {
							if f.ID == "f1" {
								want = true
							}
						}
					}
					if got := cfg.Protected("f1"); got != want {
						t.Errorf("snapshotOnly=%v archivesOnly=%v scoped=%v archive=%v: Protected=%v, but the copiers say %v",
							snapshotOnly, archivesOnly, scoped, hasArchive, got, want)
					}
				}
			}
		}
	}
}
