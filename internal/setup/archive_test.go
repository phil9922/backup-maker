// SPDX-License-Identifier: MIT

package setup

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/phil9922/backup-maker/internal/config"
)

// STOPPING A SCHEDULE IS NOT DELETING BACKUPS. Same promise as removing a
// folder or a destination: the zips it already wrote stay where they are and
// stay openable with the password that made them.
func TestRemovingAScheduleLeavesItsSnapshotsAlone(t *testing.T) {
	isolate(t)
	base := t.TempDir()
	src := mustDir(t, base, "src")
	dest := mustDir(t, base, "dest")
	folder, targets, err := CreateBackup(BackupRequest{Path: src, Destinations: []Destination{{Path: dest}}})
	if err != nil {
		t.Fatal(err)
	}
	if err := AddArchive("nightly", []string{folder.ID}, "daily", targets[0].Name, 3, "pw"); err != nil {
		t.Fatal(err)
	}
	// A snapshot it already wrote.
	zipDir := filepath.Join(dest, "backup-maker-archives", "m", "nightly")
	if err := os.MkdirAll(zipDir, 0o755); err != nil {
		t.Fatal(err)
	}
	zip := filepath.Join(zipDir, "nightly-20260101-000000.zip")
	if err := os.WriteFile(zip, []byte("sealed"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := RemoveArchive("nightly"); err != nil {
		t.Fatal(err)
	}

	if len(load(t).Archives) != 0 {
		t.Error("the schedule survived being stopped")
	}
	if _, err := os.Stat(zip); err != nil {
		t.Errorf("stopping a schedule deleted a snapshot it had already written: %v", err)
	}
	// The password goes with the job, and only after the config change landed.
	state, err := config.LoadState()
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := state.ArchivePasswords["nightly"]; ok {
		t.Error("the stored password outlived the job that used it")
	}
}

// Pausing keeps everything — above all the password, which by design cannot be
// recovered. That is the whole reason pause exists separately from stop.
func TestPausingAScheduleKeepsItsPasswordAndSettings(t *testing.T) {
	isolate(t)
	base := t.TempDir()
	src := mustDir(t, base, "src")
	dest := mustDir(t, base, "dest")
	folder, targets, err := CreateBackup(BackupRequest{Path: src, Destinations: []Destination{{Path: dest}}})
	if err != nil {
		t.Fatal(err)
	}
	if err := AddArchive("nightly", []string{folder.ID}, "daily", targets[0].Name, 3, "pw"); err != nil {
		t.Fatal(err)
	}

	if err := SetArchivePaused("nightly", true); err != nil {
		t.Fatal(err)
	}

	cfg := load(t)
	if !cfg.Archives[0].Paused {
		t.Fatal("the schedule was not paused")
	}
	if cfg.Archives[0].Every != "daily" || cfg.Archives[0].Keep != 3 {
		t.Errorf("pausing changed the schedule itself: %+v", cfg.Archives[0])
	}
	if len(cfg.Archives[0].Folders) != 1 {
		t.Error("pausing lost the folder the job covers")
	}
	state, _ := config.LoadState()
	if state.ArchivePasswords["nightly"] != "pw" {
		t.Error("pausing threw away the password, which cannot be recovered")
	}

	if err := SetArchivePaused("nightly", false); err != nil {
		t.Fatal(err)
	}
	if load(t).Archives[0].Paused {
		t.Error("the schedule did not resume")
	}
}

// An interval that means nothing is refused rather than written, or the job
// would silently stop running.
func TestChangingAScheduleRefusesAnUnusableInterval(t *testing.T) {
	isolate(t)
	base := t.TempDir()
	src := mustDir(t, base, "src")
	dest := mustDir(t, base, "dest")
	folder, targets, err := CreateBackup(BackupRequest{Path: src, Destinations: []Destination{{Path: dest}}})
	if err != nil {
		t.Fatal(err)
	}
	if err := AddArchive("nightly", []string{folder.ID}, "daily", targets[0].Name, 3, "pw"); err != nil {
		t.Fatal(err)
	}

	if err := SetArchiveSchedule("nightly", "whenever", 0, nil); err == nil {
		t.Error("an unusable interval was accepted")
	}
	if got := load(t).Archives[0].Every; got != "daily" {
		t.Errorf("a refused edit still changed the schedule to %q", got)
	}

	if err := SetArchiveSchedule("nightly", "weekly", 7, nil); err != nil {
		t.Fatal(err)
	}
	cfg := load(t)
	if cfg.Archives[0].Every != "weekly" || cfg.Archives[0].Keep != 7 {
		t.Errorf("the edit did not apply: %+v", cfg.Archives[0])
	}
}
