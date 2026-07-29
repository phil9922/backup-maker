// SPDX-License-Identifier: MIT

package setup

import (
	"testing"

	"github.com/phil9922/backup-maker/internal/config"
)

func loadTarget(t *testing.T, name string) config.Target {
	t.Helper()
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	for _, target := range cfg.Targets {
		if target.Name == name {
			return target
		}
	}
	t.Fatalf("no target named %q", name)
	return config.Target{}
}

// Asking for a timed snapshot must not also start mirroring, even when a
// destination that mirrors "every folder" is already configured — which is the
// normal state of any machine that has ever backed up a folder.
func TestATimedBackupDoesNotStartMirroringToExistingTargets(t *testing.T) {
	isolate(t)
	base := t.TempDir()
	first := mustDir(t, base, "development")
	dest := mustDir(t, base, "card")

	// The every-folder case, which is the one that bites: a destination added
	// on its own — as `add-target` does, and as any destination that predates
	// per-folder scoping is — carries no folder list, so it mirrors
	// everything. Building it with CreateBackup instead would scope it to the
	// one folder and the bug could not appear, which is exactly how an earlier
	// version of this test passed while the fault was still present.
	target, err := AddDriveTarget(dest, "card")
	if err != nil {
		t.Fatal(err)
	}
	if len(target.Folders) != 0 {
		t.Fatalf("the destination was scoped to %v; this test needs the every-folder case", target.Folders)
	}
	if _, _, err := CreateBackup(BackupRequest{
		Path: first, Destinations: []Destination{{ExistingTarget: target.Name}},
	}); err != nil {
		t.Fatal(err)
	}
	if got := loadTarget(t, target.Name); len(got.Folders) != 0 {
		t.Fatalf("the destination became scoped to %v; the every-folder case is what this test needs", got.Folders)
	}

	// Now a timed snapshot of a second folder, sent to that same destination —
	// which is what somebody with one drive already set up would do.
	desktop := mustDir(t, base, "desktop")
	folder, _, err := CreateBackup(BackupRequest{
		Path:         desktop,
		Mode:         ModeTimed,
		Destinations: []Destination{{ExistingTarget: target.Name}},
		Archive:      &ArchiveSpec{Name: "nightly", Every: "daily", Keep: 3, Password: "pw"},
	})
	if err != nil {
		t.Fatal(err)
	}

	if !folderByID(t, folder.ID).SnapshotOnly {
		t.Error("a folder created for a timed backup is not marked snapshot-only")
	}
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	for _, target := range cfg.Targets {
		for _, f := range cfg.FoldersForTarget(target) {
			if f.ID == folder.ID {
				t.Fatalf("%q is mirroring %q, which was asked for as a scheduled snapshot only", target.Name, f.Label)
			}
		}
	}
}

// The way back: giving a snapshot-only folder an incremental backup is a
// person asking for the mirror, so the flag clears and it is mirrored again.
func TestAddingAnIncrementalBackupToASnapshotOnlyFolderStartsMirroringIt(t *testing.T) {
	isolate(t)
	base := t.TempDir()
	desktop := mustDir(t, base, "desktop")
	dest := mustDir(t, base, "card")

	folder, _, err := CreateBackup(BackupRequest{
		Path:         desktop,
		Mode:         ModeTimed,
		Destinations: []Destination{{Path: dest}},
		Archive:      &ArchiveSpec{Name: "nightly", Every: "daily", Keep: 3, Password: "pw"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !folderByID(t, folder.ID).SnapshotOnly {
		t.Fatal("setup failed: the folder should start out snapshot-only")
	}

	dest2 := mustDir(t, base, "card2")
	if _, _, err := CreateBackup(BackupRequest{
		FolderID: folder.ID, Destinations: []Destination{{Path: dest2}},
	}); err != nil {
		t.Fatal(err)
	}

	if folderByID(t, folder.ID).SnapshotOnly {
		t.Fatal("asking for an incremental backup left the folder marked snapshot-only, so it still will not be mirrored")
	}
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	mirrored := false
	for _, target := range cfg.Targets {
		for _, f := range cfg.FoldersForTarget(target) {
			if f.ID == folder.ID {
				mirrored = true
			}
		}
	}
	if !mirrored {
		t.Error("after asking for an incremental backup the folder is still mirrored by nothing")
	}
}

// An ordinary incremental backup is untouched by any of this.
func TestAnIncrementalBackupIsNeverMarkedSnapshotOnly(t *testing.T) {
	isolate(t)
	base := t.TempDir()
	src := mustDir(t, base, "src")
	dest := mustDir(t, base, "dest")

	folder, _, err := CreateBackup(BackupRequest{Path: src, Destinations: []Destination{{Path: dest}}})
	if err != nil {
		t.Fatal(err)
	}
	if folderByID(t, folder.ID).SnapshotOnly {
		t.Fatal("an ordinary incremental backup was marked snapshot-only")
	}
}

// Stopping a snapshot-only folder and turning it back on must restore the KIND
// of backup it had. Without this the folder returns unmarked, every
// destination that mirrors "every folder" claims it, and the unasked-for
// mirror is back — reintroduced by the very button offered to fix things.
func TestTurningASnapshotOnlyFolderBackOnDoesNotStartMirroringIt(t *testing.T) {
	isolate(t)
	base := t.TempDir()
	src := mustDir(t, base, "desktop")
	dest := mustDir(t, base, "card")

	// An every-folder destination, then a timed-only folder alongside it.
	target, err := AddDriveTarget(dest, "card")
	if err != nil {
		t.Fatal(err)
	}
	folder, _, err := CreateBackup(BackupRequest{
		Path:         src,
		Mode:         ModeTimed,
		Destinations: []Destination{{ExistingTarget: target.Name}},
		Archive:      &ArchiveSpec{Name: "nightly", Every: "daily", Keep: 3, Password: "pw"},
	})
	if err != nil {
		t.Fatal(err)
	}

	if err := RemoveFolder(folder.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := ReenableFolder(folder.ID); err != nil {
		t.Fatal(err)
	}

	if !folderByID(t, folder.ID).SnapshotOnly {
		t.Error("the folder came back without its snapshot-only marking")
	}
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	for _, tgt := range cfg.Targets {
		for _, f := range cfg.FoldersForTarget(tgt) {
			if f.ID == folder.ID {
				t.Fatalf("%q started mirroring a snapshot-only folder that was turned back on", tgt.Name)
			}
		}
	}
}

// THE GUARANTEE: deleting a folder's last schedule leaves the folder listed,
// still snapshot-only, and honestly reported as backed up by nothing — it never
// quietly re-mirrors it to make the state look tidy.
//
// This is the state a real machine was found in on 2026-07-28. Every step was
// individually correct: SnapshotOnly kept the folder out of the every-folder
// destinations, and RemoveArchive left the folder alone because removing a
// schedule must never delete anything else. The result was a folder nothing
// copied, which the wizard still offered as one "you're already protecting".
//
// The tempting repair is to clear SnapshotOnly here so the unscoped
// destinations pick the folder up again. That is the incident this file is
// named after, in reverse: it would start a continuous mirror of the folder
// onto every drive configured, without anyone asking for one.
func TestDeletingTheLastScheduleLeavesTheFolderUnprotectedAndSnapshotOnly(t *testing.T) {
	isolate(t)
	base := t.TempDir()
	desktop := mustDir(t, base, "desktop")
	dest := mustDir(t, base, "card")

	folder, _, err := CreateBackup(BackupRequest{
		Path:         desktop,
		Mode:         ModeTimed,
		Destinations: []Destination{{Path: dest}},
		Archive:      &ArchiveSpec{Name: "nightly", Every: "daily", Keep: 3, Password: "pw"},
	})
	if err != nil {
		t.Fatal(err)
	}
	before, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	if !before.Protected(folder.ID) {
		t.Fatal("setup failed: the folder should be protected by its schedule")
	}

	if err := RemoveArchive("nightly"); err != nil {
		t.Fatal(err)
	}

	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	if folderByID(t, folder.ID).ID == "" {
		t.Fatal("deleting the schedule removed the folder; removing a schedule must not remove anything else")
	}
	if !folderByID(t, folder.ID).SnapshotOnly {
		t.Error("SnapshotOnly was cleared, which hands the folder to every destination that mirrors every folder")
	}
	if cfg.Protected(folder.ID) {
		t.Error("the folder reports as protected with no schedule and no mirror")
	}
	if cfg.Mirrored(folder.ID) {
		t.Error("deleting the schedule started mirroring the folder")
	}
	bare := cfg.UnprotectedFolders()
	if len(bare) != 1 || bare[0].ID != folder.ID {
		t.Errorf("UnprotectedFolders() = %d folders, want exactly the one whose schedule was deleted", len(bare))
	}

	// The destination is untouched too: no folder list was narrowed, and none
	// was emptied — an empty list would mean EVERY folder.
	for _, target := range cfg.Targets {
		for _, b := range before.Targets {
			if b.Name != target.Name {
				continue
			}
			if len(target.Folders) != len(b.Folders) || target.ArchivesOnly != b.ArchivesOnly {
				t.Errorf("destination %q changed scope when a schedule was deleted: %+v -> %+v", target.Name, b, target)
			}
		}
	}
}
