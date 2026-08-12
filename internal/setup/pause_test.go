// SPDX-License-Identifier: MIT

package setup

import (
	"strings"
	"testing"

	"github.com/phil9922/backup-maker/internal/config"
)

// THE TRAP THIS FEATURE IS ONE STEP AWAY FROM, and the reason the pause is
// recorded where it is.
//
// An empty Target.Folders list means EVERY folder. The obvious way to pause a
// mirror — take the folder out of the destination's list — empties that list for
// a destination scoped to one folder, and hands it every folder on the machine
// instead of none. That has been shipped twice and nearly shipped a third time.
//
// So the pause is a list of DESTINATION NAMES on the FOLDER, whose empty case
// means nothing is paused, and the destination's own scope must come out of a
// pause and a resume completely unchanged.
func TestPausingAMirrorNeverEmptiesADestinationsFolderList(t *testing.T) {
	isolate(t)
	base := t.TempDir()
	src := mustDir(t, base, "desktop")
	other := mustDir(t, base, "development")

	folder, targets, err := CreateBackup(BackupRequest{
		Path: src, Destinations: []Destination{{Path: mustDir(t, base, "card")}},
	})
	if err != nil {
		t.Fatal(err)
	}
	scoped := loadTarget(t, targets[0].Name)
	if len(scoped.Folders) != 1 {
		t.Fatalf("this test needs a destination scoped to one folder, got %v", scoped.Folders)
	}
	// A folder that destination must never start mirroring.
	second, _, err := CreateBackup(BackupRequest{
		Path: other, Destinations: []Destination{{Path: mustDir(t, base, "card2")}},
	})
	if err != nil {
		t.Fatal(err)
	}

	if err := SetMirrorPaused(folder.ID, scoped.Name, true); err != nil {
		t.Fatal(err)
	}

	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	after := loadTarget(t, scoped.Name)
	if len(after.Folders) != 1 || after.Folders[0] != folder.ID {
		t.Fatalf("pausing changed the destination's scope: %v (was %v) — an empty list here means EVERY folder",
			after.Folders, scoped.Folders)
	}
	for _, f := range cfg.FoldersForTarget(after) {
		if f.ID == second.ID {
			t.Fatalf("%q was widened to mirror %q by a pause", after.Name, f.Label)
		}
	}
	if !cfg.MirrorPaused(folder.ID, scoped.Name) {
		t.Error("the pause was not recorded at all")
	}

	// And the resume, which is where a list of this kind usually empties.
	if err := SetMirrorPaused(folder.ID, scoped.Name, false); err != nil {
		t.Fatal(err)
	}
	cfg, err = config.Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.MirrorPaused(folder.ID, scoped.Name) {
		t.Error("the pair is still paused after being resumed")
	}
	if again := loadTarget(t, scoped.Name); len(again.Folders) != 1 {
		t.Fatalf("resuming changed the destination's scope: %v", again.Folders)
	}
}

// Pausing one destination must leave the folder's OTHER destinations copying.
// The whole point of a per-pair pause is that it is per pair.
func TestPausingOneDestinationLeavesTheOthersRunning(t *testing.T) {
	isolate(t)
	base := t.TempDir()
	src := mustDir(t, base, "desktop")

	folder, targets, err := CreateBackup(BackupRequest{
		Path: src,
		Destinations: []Destination{
			{Path: mustDir(t, base, "card")},
			{Path: mustDir(t, base, "usb")},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(targets) != 2 {
		t.Fatalf("this test needs two destinations, got %d", len(targets))
	}

	if err := SetMirrorPaused(folder.ID, targets[0].Name, true); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.MirrorPaused(folder.ID, targets[0].Name) {
		t.Error("the destination that was paused is not paused")
	}
	if cfg.MirrorPaused(folder.ID, targets[1].Name) {
		t.Errorf("pausing %q also paused %q", targets[0].Name, targets[1].Name)
	}
	// And the folder is still mirrored to both as far as the configuration is
	// concerned: pausing is not un-configuring, and the copies on the paused
	// destination are still that folder's copies.
	for _, name := range []string{targets[0].Name, targets[1].Name} {
		if !mirrors(cfg, folder.ID, name) {
			t.Errorf("%q no longer holds a mirror of the folder at all", name)
		}
	}
}

// Renaming a destination has to carry the pause with it. Missing it would leave
// the pause pointing at a name nothing answers to, and the mirror would quietly
// start copying again under the new one.
func TestRenamingADestinationCarriesThePauseWithIt(t *testing.T) {
	isolate(t)
	base := t.TempDir()
	folder, targets, err := CreateBackup(BackupRequest{
		Path: mustDir(t, base, "desktop"), Destinations: []Destination{{Path: mustDir(t, base, "card")}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := SetMirrorPaused(folder.ID, targets[0].Name, true); err != nil {
		t.Fatal(err)
	}
	if err := RenameTarget(targets[0].Name, "laptopcard"); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.MirrorPaused(folder.ID, "laptopcard") {
		t.Error("the rename left the pause behind, so the mirror silently resumed")
	}
	if cfg.MirrorPaused(folder.ID, targets[0].Name) {
		t.Error("the pause is still filed under the old name")
	}
}

// Removing a destination takes its pause with it, so a destination added later
// under the same name is not silently born switched off.
func TestRemovingADestinationTakesItsPauseWithIt(t *testing.T) {
	isolate(t)
	base := t.TempDir()
	folder, targets, err := CreateBackup(BackupRequest{
		Path: mustDir(t, base, "desktop"),
		Destinations: []Destination{
			{Path: mustDir(t, base, "card")},
			{Path: mustDir(t, base, "usb")},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := SetMirrorPaused(folder.ID, targets[0].Name, true); err != nil {
		t.Fatal(err)
	}
	if err := RemoveTarget(targets[0].Name); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.MirrorPaused(folder.ID, targets[0].Name) {
		t.Error("the removed destination's pause is still recorded on the folder")
	}
	for _, f := range cfg.Folders {
		if len(f.PausedTargets) != 0 {
			t.Errorf("%q still lists paused destinations %v", f.Label, f.PausedTargets)
		}
	}
}

// A pause has to name a mirror that exists. Recording one for a pair nothing
// copies would answer "done" to somebody stopping a backup that was never
// running, and leave a name in the config that nothing ever reads again.
func TestPausingSomethingThatIsNotBeingMirroredIsRefused(t *testing.T) {
	isolate(t)
	base := t.TempDir()
	folder, targets, err := CreateBackup(BackupRequest{
		Path: mustDir(t, base, "desktop"), Destinations: []Destination{{Path: mustDir(t, base, "card")}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := SetMirrorPaused(folder.ID, "no-such-destination", true); err == nil {
		t.Error("pausing a mirror that does not exist was accepted")
	}
	if err := SetMirrorPaused("no-such-folder", targets[0].Name, true); err == nil {
		t.Error("pausing a folder that does not exist was accepted")
	}
	// Resuming, though, must always be possible: a stale name is exactly what
	// somebody would be trying to clear.
	if err := SetMirrorPaused(folder.ID, "no-such-destination", false); err != nil {
		t.Errorf("resuming a destination that is gone was refused: %v", err)
	}
	if err := SetMirrorPaused(folder.ID, "", true); err == nil ||
		!strings.Contains(err.Error(), "which destination") {
		t.Errorf("a request with no destination was not refused clearly: %v", err)
	}
}
