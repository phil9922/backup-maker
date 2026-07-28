// SPDX-License-Identifier: MIT

package setup

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/phil9922/backup-maker/internal/config"
	"github.com/phil9922/backup-maker/internal/localmirror"
)

// driveWith builds a real directory standing in for a mounted drive, marked as
// this machine's target so the recognition gate passes, and seeded with one
// folder's mirror plus a file in its version history.
func driveWith(t *testing.T, machine, label string) string {
	t.Helper()
	root := t.TempDir()
	if err := EnsureTargetMarker(localmirror.NewLocalFS(root), "laptocard", machine); err != nil {
		t.Fatal(err)
	}
	writeUnder(t, root, config.DestRoot(machine, label)+"/notes.txt", "live copy")
	writeUnder(t, root, config.VersionRoot(machine, label)+"/notes~20260101-000000.txt", "old copy")
	return root
}

func writeUnder(t *testing.T, root, rel, body string) {
	t.Helper()
	p := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func exists(t *testing.T, root, rel string) bool {
	t.Helper()
	_, err := os.Stat(filepath.Join(root, filepath.FromSlash(rel)))
	return err == nil
}

// opener returns a TargetOpener over a real directory.
func opener(root string) TargetOpener {
	return func(config.Target) (localmirror.Backend, error) {
		return localmirror.NewLocalFS(root), nil
	}
}

// seedForDelete writes a config whose only folder has been stopped, with its
// copy on the drive at root, and returns the retired record's id.
func seedForDelete(t *testing.T, root string) {
	t.Helper()
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	cfg.General.MachineName = "my-laptop"
	cfg.Folders = []config.Folder{{ID: "f1", Path: "/home/pk/Development", Label: "development"}}
	cfg.Targets = []config.Target{{Type: "drive", Name: "laptocard", Path: root}}
	if err := cfg.Save(); err != nil {
		t.Fatal(err)
	}
	// The UUID EnsureTargetMarker recorded is what the recognition gate checks.
	if err := RemoveFolder("f1"); err != nil {
		t.Fatal(err)
	}
}

// The headline: both the mirror AND the version history go. Deleting only the
// first leaves thirty days of the file's previous contents on the disk, which
// is not "deleted" in any sense the user meant.
func TestDeletingRetiredBackupsRemovesTheMirrorAndTheVersionHistory(t *testing.T) {
	isolate(t)
	root := driveWith(t, "my-laptop", "development")
	seedForDelete(t, root)

	res, err := DeleteRetiredBackups("f1", "development", opener(root))
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Outcomes) != 1 || !res.Outcomes[0].Removed {
		t.Fatalf("the copy was not removed: %+v", res.Outcomes)
	}
	if exists(t, root, config.DestRoot("my-laptop", "development")) {
		t.Error("the live mirror is still there")
	}
	if exists(t, root, config.VersionRoot("my-laptop", "development")) {
		t.Error("the version history is still there — thirty days of previous file contents survived a 'permanent' delete")
	}
	loaded, _ := config.Load()
	if len(loaded.Retired) != 0 {
		t.Error("the record survived a complete delete")
	}
}

// A destination holds every machine's and every folder's backups. Deleting one
// folder's must not take a neighbour's with it.
func TestDeletingRetiredBackupsLeavesOtherFoldersOnTheSameDestinationAlone(t *testing.T) {
	isolate(t)
	root := driveWith(t, "my-laptop", "development")
	writeUnder(t, root, config.DestRoot("my-laptop", "photos")+"/holiday.jpg", "keep me")
	writeUnder(t, root, config.VersionRoot("my-laptop", "photos")+"/holiday~20260101-000000.jpg", "keep me too")
	writeUnder(t, root, config.DestRoot("other-machine", "development")+"/theirs.txt", "not ours")
	seedForDelete(t, root)

	if _, err := DeleteRetiredBackups("f1", "development", opener(root)); err != nil {
		t.Fatal(err)
	}

	for _, keep := range []string{
		config.DestRoot("my-laptop", "photos") + "/holiday.jpg",
		config.VersionRoot("my-laptop", "photos") + "/holiday~20260101-000000.jpg",
		config.DestRoot("other-machine", "development") + "/theirs.txt",
	} {
		if !exists(t, root, keep) {
			t.Errorf("deleting one folder's backups also removed %s", keep)
		}
	}
}

// THE TYPED NAME IS ENFORCED HERE, not in the browser. Every file must survive
// a wrong one.
func TestDeletingRetiredBackupsRefusesWithoutTheTypedName(t *testing.T) {
	isolate(t)
	root := driveWith(t, "my-laptop", "development")
	seedForDelete(t, root)

	for _, wrong := range []string{"", "Development", "dev", "  "} {
		if _, err := DeleteRetiredBackups("f1", wrong, opener(root)); err == nil {
			t.Errorf("a delete went ahead with confirmation %q", wrong)
		}
		if !exists(t, root, config.DestRoot("my-laptop", "development")+"/notes.txt") {
			t.Fatalf("files were deleted despite the confirmation %q not matching", wrong)
		}
	}
	loaded, _ := config.Load()
	if len(loaded.Retired) != 1 {
		t.Error("a refused delete still dropped the record")
	}
}

// THE DATA-LOSS REGRESSION. Retire "development", then protect a different
// directory that also resolves to <machine>/development — the live folder is
// now mirroring into the very tree the record names, and deleting "the retired
// folder's backups" would delete a backup something is actively maintaining.
func TestDeletingRetiredBackupsRefusesWhenALiveFolderUsesTheSameDestination(t *testing.T) {
	isolate(t)
	root := driveWith(t, "my-laptop", "development")
	seedForDelete(t, root)
	cfg, _ := config.Load()
	cfg.Folders = []config.Folder{{ID: "f2", Path: "/home/pk/Elsewhere", Label: "development"}}
	if err := cfg.Save(); err != nil {
		t.Fatal(err)
	}

	if _, err := DeleteRetiredBackups("f1", "development", opener(root)); err == nil {
		t.Fatal("deleted a directory a live folder is mirroring into — that is a working backup")
	}
	if !exists(t, root, config.DestRoot("my-laptop", "development")+"/notes.txt") {
		t.Error("the live folder's backup was deleted")
	}
}

// Foreign storage at a familiar mount point is how a recursive delete finds
// somebody else's files. The marker must match before anything is removed.
func TestDeletingRetiredBackupsRefusesOnUnrecognisedStorage(t *testing.T) {
	isolate(t)
	root := driveWith(t, "my-laptop", "development")
	seedForDelete(t, root)
	// Someone else's disk, mounted where ours used to be.
	stranger := t.TempDir()
	writeUnder(t, stranger, config.DestRoot("my-laptop", "development")+"/notes.txt", "a stranger's file")

	res, err := DeleteRetiredBackups("f1", "development", opener(stranger))
	if err != nil {
		t.Fatal(err)
	}
	if res.Outcomes[0].Removed {
		t.Fatal("deleted from storage that is not the target backup-maker knows")
	}
	if !exists(t, stranger, config.DestRoot("my-laptop", "development")+"/notes.txt") {
		t.Error("a stranger's files were deleted")
	}
	if !res.RecordKept {
		t.Error("the record was dropped even though nothing was deleted")
	}
}

// config.toml is hand-editable and these paths are handed to RemoveAll. A
// record that has been edited into something dangerous is refused rather than
// cleaned up and used.
func TestDeletingRetiredBackupsRefusesAHandEditedPath(t *testing.T) {
	for _, bad := range []struct{ name, dest, versions string }{
		{"parent traversal", "../../etc", config.VersionsDirName + "/my-laptop/development"},
		{"absolute", "/etc", config.VersionsDirName + "/my-laptop/development"},
		{"one segment", "my-laptop", config.VersionsDirName + "/my-laptop/development"},
		{"empty", "", config.VersionsDirName + "/my-laptop/development"},
		{"versions outside the store", "my-laptop/development", "my-laptop/development"},
	} {
		t.Run(bad.name, func(t *testing.T) {
			isolate(t)
			root := driveWith(t, "my-laptop", "development")
			seedForDelete(t, root)
			cfg, _ := config.Load()
			cfg.Retired[0].Copies[0].DestPath = bad.dest
			cfg.Retired[0].Copies[0].VersionsPath = bad.versions
			if err := cfg.Save(); err != nil {
				t.Fatal(err)
			}

			res, err := DeleteRetiredBackups("f1", "development", opener(root))
			if err == nil && res.Outcomes[0].Removed {
				t.Fatalf("a hand-edited path (%s) was used for a recursive delete", bad.name)
			}
			if !exists(t, root, config.DestRoot("my-laptop", "development")+"/notes.txt") {
				t.Error("files were deleted through an unsafe path")
			}
		})
	}
}

// A destination that was unplugged when the button was pressed must be
// REPORTED and the record kept, so the rest can be finished later. Dropping it
// would recreate the invisible orphan this whole feature exists to end.
func TestADestinationThatCannotBeOpenedIsReportedAndTheRecordSurvives(t *testing.T) {
	isolate(t)
	root := driveWith(t, "my-laptop", "development")
	seedForDelete(t, root)

	failing := func(config.Target) (localmirror.Backend, error) {
		return nil, os.ErrNotExist
	}
	res, err := DeleteRetiredBackups("f1", "development", failing)
	if err != nil {
		t.Fatal(err)
	}
	if res.Outcomes[0].Removed {
		t.Fatal("reported a delete against a destination it could not open")
	}
	if !res.RecordKept {
		t.Fatal("the record was dropped even though the copy is still there")
	}
	loaded, _ := config.Load()
	if len(loaded.Retired) != 1 {
		t.Fatal("the record did not survive a failed delete")
	}
	if loaded.Retired[0].Copies[0].Error == "" {
		t.Error("the reason was not recorded against the copy, so the card cannot explain what happened")
	}
	if !exists(t, root, config.DestRoot("my-laptop", "development")+"/notes.txt") {
		t.Error("files went missing on a delete that failed")
	}
}

// A copy on a paired computer is on that machine's disk. It cannot be deleted
// from here, it is said so in words, and it does not stop the record being
// dropped once everything reachable is gone.
func TestACopyOnAPairedComputerIsReportedAsNotDeletableHere(t *testing.T) {
	isolate(t)
	root := driveWith(t, "my-laptop", "development")
	seedForDelete(t, root)
	cfg, _ := config.Load()
	cfg.Retired[0].Copies = append(cfg.Retired[0].Copies, config.RetiredCopy{
		Target: "attic-pi", Type: "device", DestPath: "my-laptop/development",
		VersionsPath: config.VersionsDirName + "/my-laptop/development",
	})
	if err := cfg.Save(); err != nil {
		t.Fatal(err)
	}

	res, err := DeleteRetiredBackups("f1", "development", opener(root))
	if err != nil {
		t.Fatal(err)
	}
	var skipped bool
	for _, o := range res.Outcomes {
		if o.Target == "attic-pi" {
			skipped = o.Skipped
		}
	}
	if !skipped {
		t.Error("a paired computer's copy was not reported as undeletable from here")
	}
	if res.RecordKept {
		t.Error("a copy on another computer blocked the record from being dropped; it never can be deleted from here, so it would block for ever")
	}
}

// A snapshot zip is one encrypted file per job run that may hold other folders
// too. Nothing in this path may touch one.
func TestDeletingRetiredBackupsNeverTouchesSnapshotZips(t *testing.T) {
	isolate(t)
	root := driveWith(t, "my-laptop", "development")
	writeUnder(t, root, "backup-maker-archives/my-laptop/nightly/nightly-20260101-000000.zip", "encrypted")
	seedForDelete(t, root)

	if _, err := DeleteRetiredBackups("f1", "development", opener(root)); err != nil {
		t.Fatal(err)
	}

	if !exists(t, root, "backup-maker-archives/my-laptop/nightly/nightly-20260101-000000.zip") {
		t.Error("a scheduled snapshot was deleted; it may hold other folders and cannot be edited")
	}
}

// The source folder on this computer is never the thing being deleted, and the
// dialog says so — so prove it.
func TestDeletingRetiredBackupsNeverTouchesTheSourceFolder(t *testing.T) {
	isolate(t)
	root := driveWith(t, "my-laptop", "development")
	source := t.TempDir()
	if err := os.WriteFile(filepath.Join(source, "original.txt"), []byte("mine"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, _ := config.Load()
	cfg.General.MachineName = "my-laptop"
	cfg.Folders = []config.Folder{{ID: "f1", Path: source, Label: "development"}}
	cfg.Targets = []config.Target{{Type: "drive", Name: "laptocard", Path: root}}
	if err := cfg.Save(); err != nil {
		t.Fatal(err)
	}
	if err := RemoveFolder("f1"); err != nil {
		t.Fatal(err)
	}

	if _, err := DeleteRetiredBackups("f1", "development", opener(root)); err != nil {
		t.Fatal(err)
	}

	if _, err := os.Stat(filepath.Join(source, "original.txt")); err != nil {
		t.Fatalf("the folder on this computer was deleted: %v", err)
	}
}
