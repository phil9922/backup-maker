// SPDX-License-Identifier: MIT

package setup

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/phil9922/backup-maker/internal/config"
)

// isolate points config at a throwaway directory so tests never touch the
// developer's real backup-maker configuration.
func isolate(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir) // linux
	t.Setenv("HOME", dir)            // macOS (and Roots())
	cfg := config.New()
	if err := cfg.Save(); err != nil {
		t.Fatal(err)
	}
}

func mustDir(t *testing.T, parent, name string) string {
	t.Helper()
	p := filepath.Join(parent, name)
	if err := os.MkdirAll(p, 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

func load(t *testing.T) *config.Config {
	t.Helper()
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

func TestCreateBackupWiresOneFolderToSeveralDestinations(t *testing.T) {
	isolate(t)
	base := t.TempDir()
	src := mustDir(t, base, "src")
	a := mustDir(t, base, "destA")
	b := mustDir(t, base, "destB")

	folder, targets, err := CreateBackup(BackupRequest{
		Path:         src,
		Destinations: []Destination{{Path: a}, {Path: b}},
	})
	if err != nil {
		t.Fatalf("CreateBackup: %v", err)
	}
	if len(targets) != 2 {
		t.Fatalf("got %d targets, want 2", len(targets))
	}

	cfg := load(t)
	if len(cfg.Folders) != 1 || cfg.Folders[0].Path != src {
		t.Fatalf("folder not saved: %+v", cfg.Folders)
	}
	if len(cfg.Targets) != 2 {
		t.Fatalf("got %d targets in config, want 2", len(cfg.Targets))
	}
	// Each new target must be scoped to this folder, not silently set to
	// "every folder" — that would sweep in folders the user never chose.
	for _, tg := range cfg.Targets {
		if len(tg.Folders) != 1 || tg.Folders[0] != folder.ID {
			t.Errorf("target %q scoped to %v, want [%s]", tg.Name, tg.Folders, folder.ID)
		}
	}
}

// The worst outcome is a half-applied backup: the user is told they are
// protected while one destination silently does nothing.
func TestCreateBackupRollsBackOnBadDestination(t *testing.T) {
	isolate(t)
	base := t.TempDir()
	src := mustDir(t, base, "src")
	good := mustDir(t, base, "good")

	before := load(t)
	_, _, err := CreateBackup(BackupRequest{
		Path: src,
		Destinations: []Destination{
			{Path: good},
			{Path: filepath.Join(base, "does-not-exist")},
		},
	})
	if err == nil {
		t.Fatal("expected an error for the missing destination")
	}

	after := load(t)
	if len(after.Folders) != len(before.Folders) {
		t.Errorf("folder was written despite failure: %+v", after.Folders)
	}
	if len(after.Targets) != len(before.Targets) {
		t.Errorf("target was written despite failure: %+v", after.Targets)
	}
}

func TestCreateBackupRequiresADestination(t *testing.T) {
	isolate(t)
	src := mustDir(t, t.TempDir(), "src")
	if _, _, err := CreateBackup(BackupRequest{Path: src}); err == nil {
		t.Error("a backup with nowhere to go was accepted")
	}
}

func TestCreateBackupReusesExistingTarget(t *testing.T) {
	isolate(t)
	base := t.TempDir()
	first := mustDir(t, base, "src1")
	second := mustDir(t, base, "src2")
	dest := mustDir(t, base, "dest")

	f1, targets, err := CreateBackup(BackupRequest{
		Path: first, Destinations: []Destination{{Path: dest}},
	})
	if err != nil {
		t.Fatal(err)
	}
	name := targets[0].Name

	f2, _, err := CreateBackup(BackupRequest{
		Path: second, Destinations: []Destination{{ExistingTarget: name}},
	})
	if err != nil {
		t.Fatalf("reusing an existing target: %v", err)
	}

	cfg := load(t)
	if len(cfg.Targets) != 1 {
		t.Fatalf("target was duplicated: %+v", cfg.Targets)
	}
	got := cfg.Targets[0].Folders
	if len(got) != 2 || got[0] != f1.ID || got[1] != f2.ID {
		t.Errorf("target folders = %v, want both %s and %s", got, f1.ID, f2.ID)
	}
}

// An empty Folders list already means "every folder"; appending an id would
// narrow the target and silently stop backing up everything else.
func TestCreateBackupLeavesAllFoldersTargetAlone(t *testing.T) {
	isolate(t)
	base := t.TempDir()
	src := mustDir(t, base, "src")
	dest := mustDir(t, base, "dest")

	if _, err := AddDriveTarget(dest, "catchall"); err != nil {
		t.Fatal(err)
	}
	cfg := load(t)
	if len(cfg.Targets[0].Folders) != 0 {
		t.Fatalf("precondition: expected an all-folders target, got %v", cfg.Targets[0].Folders)
	}

	if _, _, err := CreateBackup(BackupRequest{
		Path: src, Destinations: []Destination{{ExistingTarget: "catchall"}},
	}); err != nil {
		t.Fatal(err)
	}

	after := load(t)
	if len(after.Targets[0].Folders) != 0 {
		t.Errorf("an all-folders target was narrowed to %v", after.Targets[0].Folders)
	}
}

func TestRemoveFolderStripsDanglingReferences(t *testing.T) {
	isolate(t)
	base := t.TempDir()
	src := mustDir(t, base, "src")
	dest := mustDir(t, base, "dest")

	folder, targets, err := CreateBackup(BackupRequest{
		Path: src, Destinations: []Destination{{Path: dest}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := AddArchive("snap", []string{folder.ID}, "weekly", targets[0].Name, 3, "pw"); err != nil {
		t.Fatal(err)
	}

	if err := RemoveFolder(folder.ID); err != nil {
		t.Fatal(err)
	}
	cfg := load(t)
	if len(cfg.Folders) != 0 {
		t.Errorf("folder still present: %+v", cfg.Folders)
	}
	for _, tg := range cfg.Targets {
		for _, id := range tg.Folders {
			if id == folder.ID {
				t.Errorf("target %q still references the removed folder", tg.Name)
			}
		}
	}
	for _, a := range cfg.Archives {
		for _, id := range a.Folders {
			if id == folder.ID {
				t.Errorf("archive %q still references the removed folder", a.Name)
			}
		}
	}
}

// An archive whose destination is gone can never run, so it must not linger
// looking healthy.
func TestRemoveTargetDropsItsArchives(t *testing.T) {
	isolate(t)
	base := t.TempDir()
	src := mustDir(t, base, "src")
	dest := mustDir(t, base, "dest")

	folder, targets, err := CreateBackup(BackupRequest{
		Path: src, Destinations: []Destination{{Path: dest}},
	})
	if err != nil {
		t.Fatal(err)
	}
	name := targets[0].Name
	if err := AddArchive("snap", []string{folder.ID}, "weekly", name, 3, "pw"); err != nil {
		t.Fatal(err)
	}

	if err := RemoveTarget(name); err != nil {
		t.Fatal(err)
	}
	cfg := load(t)
	if len(cfg.Targets) != 0 {
		t.Errorf("target still present: %+v", cfg.Targets)
	}
	if len(cfg.Archives) != 0 {
		t.Errorf("archive pointing at the removed target survived: %+v", cfg.Archives)
	}
	state, err := config.LoadState()
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := state.ArchivePasswords["snap"]; ok {
		t.Error("the orphaned archive's password was left behind in state.json")
	}
}

func TestAddArchiveRequiresAPassword(t *testing.T) {
	isolate(t)
	base := t.TempDir()
	src := mustDir(t, base, "src")
	dest := mustDir(t, base, "dest")

	folder, targets, err := CreateBackup(BackupRequest{
		Path: src, Destinations: []Destination{{Path: dest}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := AddArchive("snap", []string{folder.ID}, "weekly", targets[0].Name, 3, ""); err == nil {
		t.Error("an unencrypted archive was accepted; the password is mandatory")
	}
}

func TestAddFolderRejectsDuplicatesAndNonDirectories(t *testing.T) {
	isolate(t)
	base := t.TempDir()
	src := mustDir(t, base, "src")

	if _, err := AddFolder(src, "", nil, false); err != nil {
		t.Fatal(err)
	}
	if _, err := AddFolder(src, "", nil, false); err == nil {
		t.Error("the same folder was added twice")
	}

	file := filepath.Join(base, "a.txt")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := AddFolder(file, "", nil, false); err == nil {
		t.Error("a file was accepted as a backup source")
	}
}

// A timed backup keeps no live copy. If it silently created a mirror, the user
// would be getting continuous copying they explicitly didn't ask for.
func TestTimedBackupCreatesScheduleAndNoMirror(t *testing.T) {
	isolate(t)
	base := t.TempDir()
	src := mustDir(t, base, "src")
	dest := mustDir(t, base, "dest")

	folder, targets, err := CreateBackup(BackupRequest{
		Path:         src,
		Mode:         ModeTimed,
		Destinations: []Destination{{Path: dest}},
		Archive:      &ArchiveSpec{Every: "weekly", Password: "pw", Keep: 4},
	})
	if err != nil {
		t.Fatalf("CreateBackup: %v", err)
	}

	cfg := load(t)
	if !cfg.Targets[0].ArchivesOnly {
		t.Error("timed destination is not marked archives_only, so it will mirror")
	}
	if got := cfg.FoldersForTarget(cfg.Targets[0]); len(got) != 0 {
		t.Errorf("timed destination mirrors %d folder(s); it must mirror none", len(got))
	}
	if len(cfg.Archives) != 1 {
		t.Fatalf("expected one schedule, got %+v", cfg.Archives)
	}
	a := cfg.Archives[0]
	if a.Target != targets[0].Name || len(a.Folders) != 1 || a.Folders[0] != folder.ID || a.Keep != 4 {
		t.Errorf("schedule wired wrongly: %+v", a)
	}
	state, err := config.LoadState()
	if err != nil {
		t.Fatal(err)
	}
	if state.ArchivePasswords[a.Name] != "pw" {
		t.Error("archive password was not stored")
	}
}

// The schedule is a timed backup's only protection, so it must not be possible
// to save the folder without one.
func TestTimedBackupRequiresASchedule(t *testing.T) {
	isolate(t)
	base := t.TempDir()
	src := mustDir(t, base, "src")
	dest := mustDir(t, base, "dest")

	if _, _, err := CreateBackup(BackupRequest{
		Path: src, Mode: ModeTimed, Destinations: []Destination{{Path: dest}},
	}); err == nil {
		t.Fatal("a timed backup with no schedule was accepted")
	}
	cfg := load(t)
	if len(cfg.Folders) != 0 || len(cfg.Targets) != 0 {
		t.Error("the rejected backup still wrote configuration")
	}
}

func TestTimedBackupRejectsPairedMachine(t *testing.T) {
	isolate(t)
	src := mustDir(t, t.TempDir(), "src")
	if _, _, err := CreateBackup(BackupRequest{
		Path: src, Mode: ModeTimed,
		Destinations: []Destination{{DeviceID: "SOMEDEVICEID"}},
		Archive:      &ArchiveSpec{Every: "daily", Password: "pw"},
	}); err == nil {
		t.Error("snapshots were accepted onto a paired machine, which can't store them")
	}
}

// Adding a live copy to a destination that was snapshot-only should promote it
// to doing both, not leave it silently mirroring nothing.
func TestIncrementalPromotesArchivesOnlyDestination(t *testing.T) {
	isolate(t)
	base := t.TempDir()
	timedSrc := mustDir(t, base, "src1")
	liveSrc := mustDir(t, base, "src2")
	dest := mustDir(t, base, "dest")

	_, targets, err := CreateBackup(BackupRequest{
		Path: timedSrc, Mode: ModeTimed,
		Destinations: []Destination{{Path: dest}},
		Archive:      &ArchiveSpec{Every: "daily", Password: "pw"},
	})
	if err != nil {
		t.Fatal(err)
	}
	name := targets[0].Name

	folder, _, err := CreateBackup(BackupRequest{
		Path: liveSrc, Mode: ModeIncremental,
		Destinations: []Destination{{ExistingTarget: name}},
	})
	if err != nil {
		t.Fatal(err)
	}

	cfg := load(t)
	if cfg.Targets[0].ArchivesOnly {
		t.Error("destination is still archives_only, so the live copy will never run")
	}
	got := cfg.FoldersForTarget(cfg.Targets[0])
	if len(got) != 1 || got[0].ID != folder.ID {
		t.Errorf("mirror scope = %+v, want only the incremental folder", got)
	}
}

// A folder already mirrored to one destination must be able to gain a second
// kind of backup. Re-adding the path is rejected as a duplicate, so without
// this the wizard could never attach a snapshot to a folder it already
// protects — exactly the "mirror to SD card + daily snapshot to the Pi" setup.
func TestCreateBackupAttachesToAnExistingFolder(t *testing.T) {
	isolate(t)
	base := t.TempDir()
	src := mustDir(t, base, "src")
	card := mustDir(t, base, "card")
	pi := mustDir(t, base, "pi")

	folder, _, err := CreateBackup(BackupRequest{
		Path: src, Mode: ModeIncremental,
		Destinations: []Destination{{Path: card}},
	})
	if err != nil {
		t.Fatal(err)
	}

	// Second backup for the SAME folder: a timed snapshot elsewhere.
	same, _, err := CreateBackup(BackupRequest{
		FolderID: folder.ID, Mode: ModeTimed,
		Destinations: []Destination{{Path: pi}},
		Archive:      &ArchiveSpec{Every: "daily", Password: "pw", Name: "daily-src"},
	})
	if err != nil {
		t.Fatalf("attaching a second backup to an existing folder: %v", err)
	}
	if same.ID != folder.ID {
		t.Errorf("got folder %s, want the existing %s", same.ID, folder.ID)
	}

	cfg := load(t)
	if len(cfg.Folders) != 1 {
		t.Errorf("folder was duplicated: %+v", cfg.Folders)
	}
	if len(cfg.Archives) != 1 || cfg.Archives[0].Folders[0] != folder.ID {
		t.Errorf("schedule not wired to the existing folder: %+v", cfg.Archives)
	}
	// The card keeps mirroring; the Pi is snapshot-only.
	byName := map[string]config.Target{}
	for _, tg := range cfg.Targets {
		byName[tg.Name] = tg
	}
	if byName["card"].ArchivesOnly {
		t.Error("the existing mirror destination was turned into snapshots-only")
	}
	if !byName["pi"].ArchivesOnly {
		t.Error("the timed destination should be snapshots-only")
	}
}

func TestCreateBackupRejectsUnknownFolderID(t *testing.T) {
	isolate(t)
	dest := mustDir(t, t.TempDir(), "dest")
	if _, _, err := CreateBackup(BackupRequest{
		FolderID: "nope-12345", Destinations: []Destination{{Path: dest}},
	}); err == nil {
		t.Error("an unknown folder id was accepted")
	}
}

// The exclude list is shared between mirror and snapshot, so this flag is the
// only way to keep a lean mirror on a small card while still sealing a
// complete archive on a bigger drive.
func TestSnapshotCanIncludeEverything(t *testing.T) {
	isolate(t)
	base := t.TempDir()
	src := mustDir(t, base, "src")
	dest := mustDir(t, base, "dest")

	if _, _, err := CreateBackup(BackupRequest{
		Path: src, Mode: ModeTimed,
		Destinations: []Destination{{Path: dest}},
		Archive: &ArchiveSpec{
			Every: "weekly", Password: "pw", IncludeEverything: true,
		},
	}); err != nil {
		t.Fatal(err)
	}
	cfg := load(t)
	if !cfg.Archives[0].NoDefaultIgnores {
		t.Error("include-everything did not reach the saved schedule")
	}
}
