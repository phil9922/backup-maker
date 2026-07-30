// SPDX-License-Identifier: MIT

package setup

import (
	"testing"
	"time"

	"github.com/phil9922/backup-maker/internal/config"
	"github.com/phil9922/backup-maker/internal/testpath"
)

// retireCfg is one folder backed up to one drive, with the target's folder list
// supplied by the caller — the whole point of several of these tests is the
// difference between a list that names the folder and one that is empty.
func retireCfg(targetFolders []string) *config.Config {
	return &config.Config{
		General: config.General{MachineName: "my-laptop"},
		Folders: []config.Folder{{ID: "f1", Path: testpath.Abs("/home/pk/Development"), Label: "development"}},
		Targets: []config.Target{{
			Type: "drive", Name: "laptocard", Path: testpath.Abs("/media/card"), Folders: targetFolders,
		}},
	}
}

// A destination that NAMES the folder is recorded as holding a copy, and the
// link is marked explicit so turning the folder back on knows to restore it.
func TestStoppingAFolderRecordsTheDestinationThatNamedIt(t *testing.T) {
	cfg := retireCfg([]string{"f1"})

	rec := RetireRecord(cfg, cfg.Folders[0], time.Now())

	if len(rec.Copies) != 1 {
		t.Fatalf("got %d copies, want 1", len(rec.Copies))
	}
	if !rec.Copies[0].Explicit {
		t.Error("a destination that named the folder was not recorded as an explicit link")
	}
}

// THE MISTAKE THIS CATCHES, and it is the easy one to make: an empty folder
// list means EVERY folder, so reading the literal list would record no copies
// at all for exactly the destinations most likely to be holding one — and the
// backups would then be invisible to the very panel meant to find them.
func TestStoppingAFolderRecordsAnEveryFolderDestinationToo(t *testing.T) {
	cfg := retireCfg(nil)

	rec := RetireRecord(cfg, cfg.Folders[0], time.Now())

	if len(rec.Copies) != 1 {
		t.Fatalf("an every-folder destination was not recorded as holding a copy (got %d)", len(rec.Copies))
	}
	if rec.Copies[0].Explicit {
		t.Error("an every-folder destination was recorded as an explicit link; re-enabling would narrow it and cut off every folder added since")
	}
}

// A snapshot-only destination holds zips, not a mirror, so there is no
// <machine>/<label> tree on it to record or ever delete.
func TestStoppingAFolderDoesNotRecordASnapshotOnlyDestinationAsHoldingAMirror(t *testing.T) {
	cfg := retireCfg(nil)
	cfg.Targets[0].ArchivesOnly = true
	cfg.Archives = []config.Archive{{Name: "nightly", Target: "laptocard", Every: "daily"}}

	rec := RetireRecord(cfg, cfg.Folders[0], time.Now())

	if len(rec.Copies) != 0 {
		t.Errorf("a snapshot-only destination was recorded as holding a mirror: %+v", rec.Copies)
	}
	if len(rec.Archives) != 1 || rec.Archives[0].Name != "nightly" {
		t.Errorf("the snapshot job the folder belonged to was not recorded: %+v", rec.Archives)
	}
}

// The recorded paths must be the ones the mirror engine actually writes, or a
// later delete aims at nothing — or at something else. Deliberately uses a
// machine name and label that exercise the character sanitising.
func TestStoppedRecordUsesThePathsTheMirrorEngineWrites(t *testing.T) {
	cfg := retireCfg(nil)
	cfg.General.MachineName = "my:machine"
	cfg.Folders[0].Label = "a/b"

	rec := RetireRecord(cfg, cfg.Folders[0], time.Now())

	wantDest := config.DestRoot("my:machine", "a/b")
	wantVersions := config.VersionRoot("my:machine", "a/b")
	if rec.Copies[0].DestPath != wantDest {
		t.Errorf("DestPath = %q, want %q", rec.Copies[0].DestPath, wantDest)
	}
	if rec.Copies[0].VersionsPath != wantVersions {
		t.Errorf("VersionsPath = %q, want %q", rec.Copies[0].VersionsPath, wantVersions)
	}
}

// The record has to survive the file, or the orphan comes back on the next
// daemon start.
func TestRetiredEntriesSurviveASaveAndLoad(t *testing.T) {
	isolate(t)
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	cfg.General.MachineName = "my-laptop"
	cfg.Folders = []config.Folder{{ID: "f1", Path: testpath.Abs("/home/pk/Development"), Label: "development"}}
	cfg.Targets = []config.Target{{Type: "drive", Name: "laptocard", Path: testpath.Abs("/media/card")}}
	if err := cfg.Save(); err != nil {
		t.Fatal(err)
	}

	if err := RemoveFolder("f1"); err != nil {
		t.Fatal(err)
	}

	loaded, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.Retired) != 1 {
		t.Fatalf("got %d retired records after a save/load, want 1", len(loaded.Retired))
	}
	r := loaded.Retired[0]
	if r.ID != "f1" || r.Label != "development" || r.Path != testpath.Abs("/home/pk/Development") {
		t.Errorf("the record did not survive intact: %+v", r)
	}
	if len(r.Copies) != 1 || r.Copies[0].Target != "laptocard" {
		t.Errorf("the destination holding the copy was lost: %+v", r.Copies)
	}
	if len(loaded.Folders) != 0 {
		t.Error("the folder is still being backed up")
	}
}

// THE SAME ID, VERBATIM. A paired machine holds the folder under it, so a
// fresh one would stand up a second copy over there and retransfer everything.
func TestReenableRestoresTheSameFolderID(t *testing.T) {
	isolate(t)
	seedAndStop(t, nil)

	res, err := ReenableFolder("f1")
	if err != nil {
		t.Fatal(err)
	}
	if res.Folder.ID != "f1" {
		t.Errorf("folder came back with id %q, want f1 — a new id means a second copy on any paired machine", res.Folder.ID)
	}
	if res.Folder.Label != "development" {
		t.Errorf("label came back as %q; the destination directory is keyed by it, so a different label re-copies everything", res.Folder.Label)
	}
	loaded, _ := config.Load()
	if len(loaded.Retired) != 0 {
		t.Error("the record survived being turned back on")
	}
}

// An every-folder destination already covers the folder, so re-enabling must
// leave its list empty. Naming the folder would convert it to an explicitly
// scoped destination and silently stop every folder added since from reaching
// it.
func TestReenableLeavesAnEveryFolderDestinationAlone(t *testing.T) {
	isolate(t)
	seedAndStop(t, nil)

	res, err := ReenableFolder("f1")
	if err != nil {
		t.Fatal(err)
	}
	loaded, _ := config.Load()
	if len(loaded.Targets[0].Folders) != 0 {
		t.Errorf("an every-folder destination was narrowed to %v", loaded.Targets[0].Folders)
	}
	if len(res.Relinked) != 0 {
		t.Errorf("reported relinking something that needed no link: %v", res.Relinked)
	}
	if len(res.Covered) != 1 {
		t.Errorf("did not report the destination that covers it anyway: %v", res.Covered)
	}
}

// A destination that DID name the folder gets the link back, or the folder
// would come back protected by nothing.
func TestReenableRelinksADestinationThatNamedTheFolder(t *testing.T) {
	isolate(t)
	seedAndStop(t, []string{"f1"})

	if _, err := ReenableFolder("f1"); err != nil {
		t.Fatal(err)
	}

	loaded, _ := config.Load()
	if len(loaded.Targets[0].Folders) != 1 || loaded.Targets[0].Folders[0] != "f1" {
		t.Errorf("the explicit link was not restored: %v", loaded.Targets[0].Folders)
	}
}

// Refusing would strand somebody with a record they cannot act on; inventing
// the destination would be worse. It is restored, and the gap is named.
func TestReenableNamesADestinationThatHasSinceBeenRemoved(t *testing.T) {
	isolate(t)
	seedAndStop(t, []string{"f1"})
	cfg, _ := config.Load()
	cfg.Targets = nil
	if err := cfg.Save(); err != nil {
		t.Fatal(err)
	}

	res, err := ReenableFolder("f1")
	if err != nil {
		t.Fatalf("the folder should still come back: %v", err)
	}
	if len(res.Missing) != 1 || res.Missing[0] != "laptocard" {
		t.Errorf("the destination that no longer exists was not reported: %+v", res.Missing)
	}
	loaded, _ := config.Load()
	if len(loaded.Folders) != 1 {
		t.Error("the folder was not restored")
	}
}

// THE COLLISION THAT LOSES DATA. Two folders resolving to one <machine>/<label>
// share a destination directory, and each mirror pass versions away everything
// under its root that is not in its own source — they would take turns deleting
// each other's files for as long as both existed.
func TestReenableRefusesWhenTheLabelWouldCollideWithALiveFolder(t *testing.T) {
	isolate(t)
	seedAndStop(t, nil)
	cfg, _ := config.Load()
	cfg.Folders = append(cfg.Folders, config.Folder{ID: "f2", Path: testpath.Abs("/home/pk/Other"), Label: "development"})
	if err := cfg.Save(); err != nil {
		t.Fatal(err)
	}

	_, err := ReenableFolder("f1")
	if err == nil {
		t.Fatal("re-enabling into a directory a live folder is already mirroring into was allowed; the two would delete each other's files")
	}
	loaded, _ := config.Load()
	if len(loaded.Retired) != 1 {
		t.Error("a refused re-enable still consumed the record")
	}
}

// Turning it back on when that path is protected again is not a merge, it is a
// duplicate — and the answer is to forget the record instead.
func TestReenableRefusesWhenThatPathIsProtectedAgain(t *testing.T) {
	isolate(t)
	seedAndStop(t, nil)
	cfg, _ := config.Load()
	cfg.Folders = append(cfg.Folders, config.Folder{ID: "f2", Path: testpath.Abs("/home/pk/Development"), Label: "dev-again"})
	if err := cfg.Save(); err != nil {
		t.Fatal(err)
	}

	if _, err := ReenableFolder("f1"); err == nil {
		t.Error("re-enabling a path that is already being backed up was allowed")
	}
}

// Forgetting is the non-destructive escape hatch, and it must stay that way:
// the record goes, the files do not.
func TestForgettingARecordTouchesNoConfiguredBackups(t *testing.T) {
	isolate(t)
	seedAndStop(t, nil)

	if err := ForgetRetired("f1"); err != nil {
		t.Fatal(err)
	}

	loaded, _ := config.Load()
	if len(loaded.Retired) != 0 {
		t.Error("the record was not forgotten")
	}
	if len(loaded.Targets) != 1 {
		t.Error("forgetting a record removed a destination")
	}
}

// Stopping the same folder twice must not leave two records describing one
// copy — the second delete would then aim at files the first already removed.
func TestStoppingAFolderTwiceKeepsOneRecord(t *testing.T) {
	isolate(t)
	seedAndStop(t, nil)
	if _, err := ReenableFolder("f1"); err != nil {
		t.Fatal(err)
	}
	if err := RemoveFolder("f1"); err != nil {
		t.Fatal(err)
	}

	loaded, _ := config.Load()
	if len(loaded.Retired) != 1 {
		t.Errorf("got %d records for one folder, want 1", len(loaded.Retired))
	}
}

// seedAndStop writes a one-folder one-drive config and stops the folder,
// leaving exactly one retired record behind.
func seedAndStop(t *testing.T, targetFolders []string) {
	t.Helper()
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	cfg.General.MachineName = "my-laptop"
	cfg.Folders = []config.Folder{{ID: "f1", Path: testpath.Abs("/home/pk/Development"), Label: "development"}}
	cfg.Targets = []config.Target{{
		Type: "drive", Name: "laptocard", Path: testpath.Abs("/media/card"), Folders: targetFolders,
	}}
	if err := cfg.Save(); err != nil {
		t.Fatal(err)
	}
	if err := RemoveFolder("f1"); err != nil {
		t.Fatal(err)
	}
}
