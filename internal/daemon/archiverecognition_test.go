// SPDX-License-Identifier: MIT

package daemon

import (
	"bytes"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/phil9922/backup-maker/internal/config"
	"github.com/phil9922/backup-maker/internal/localmirror"
)

// archiveFixture is a daemon with one snapshot job pointed at one drive.
func archiveFixture(t *testing.T, dest, recordedUUID string) (*daemon, *config.Config, config.Archive, *bytes.Buffer) {
	t.Helper()
	isolateState(t)
	src := t.TempDir()
	if err := os.WriteFile(filepath.Join(src, "a.txt"), []byte("alpha"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg := config.New()
	cfg.General.MachineName = "workstation"
	cfg.Folders = []config.Folder{{ID: "f1", Path: src, Label: "proj"}}
	cfg.Targets = []config.Target{{Type: "drive", Name: "card", Path: dest}}
	job := config.Archive{Name: "nightly", Every: "daily", Target: "card", Keep: 2}
	cfg.Archives = []config.Archive{job}

	logs := &bytes.Buffer{}
	d := &daemon{
		log: slog.New(slog.NewTextHandler(logs, nil)),
		state: &config.State{
			DriveTargetUUIDs: map[string]string{"card": recordedUUID},
			ArchivePasswords: map[string]string{"nightly": "hunter2"},
		},
		cfg: cfg,
	}
	return d, cfg, job, logs
}

func archiveResultFor(t *testing.T, d *daemon, name string) (errText string, ok bool) {
	t.Helper()
	d.mu.Lock()
	defer d.mu.Unlock()
	r, present := d.archiveResults[name]
	if !present {
		t.Fatalf("no result was recorded for %q", name)
	}
	return r.Err, r.Err == ""
}

// A REFORMATTED destination is mounted, readable, and has no identity marker on
// it. The snapshot writer used to call that "target offline" — sending the user
// to check a cable when the truth is that the storage at that location is not
// the storage they set up. The mirror beside it has said "different storage"
// about the very same drive since 2026-07-26; the two must not disagree, which
// is why this path now asks localmirror.Recognize rather than comparing the
// marker itself.
func TestASnapshotToReformattedStorageSaysWhatIsActuallyWrong(t *testing.T) {
	dest := t.TempDir() // mounted and readable, and now blank
	d, cfg, job, _ := archiveFixture(t, dest, "our-uuid")

	d.runArchive(cfg, job)

	errText, ok := archiveResultFor(t, d, "nightly")
	if ok {
		t.Fatal("a snapshot was written to storage the mirror refuses to write to")
	}
	if errText == "target offline" {
		t.Error("a mounted, readable, reformatted destination was reported as offline: " +
			"the user is sent to look for an unplugged cable instead of at the drive")
	}
	if errText != "different storage at target location; refusing to write" {
		t.Errorf("unexpected refusal %q", errText)
	}
	if names, _ := os.ReadDir(dest); len(names) != 0 {
		t.Errorf("unrecognised storage received %d entries", len(names))
	}
}

// A drive that belongs to somebody else must be refused too, and for the same
// stated reason.
func TestASnapshotToForeignStorageIsRefused(t *testing.T) {
	dest := t.TempDir()
	if err := localmirror.WriteMarkerAt(dest, "somebody-elses-uuid", "their-laptop"); err != nil {
		t.Fatal(err)
	}
	d, cfg, job, _ := archiveFixture(t, dest, "our-uuid")

	d.runArchive(cfg, job)

	errText, ok := archiveResultFor(t, d, "nightly")
	if ok {
		t.Fatal("a snapshot was written to a stranger's drive")
	}
	if errText != "different storage at target location; refusing to write" {
		t.Errorf("unexpected refusal %q", errText)
	}
	entries, _ := os.ReadDir(dest)
	if len(entries) != 1 || entries[0].Name() != localmirror.MarkerName {
		t.Errorf("files landed on a stranger's drive: %v", entries)
	}
}

// A destination that is genuinely not plugged in is ordinary, and must still be
// described as such — the Offline/Foreign split is the only thing that decides
// what the user is told, and collapsing it would make the message above useless.
func TestASnapshotToAnUnpluggedDestinationSaysOffline(t *testing.T) {
	dest := filepath.Join(t.TempDir(), "not-mounted")
	d, cfg, job, _ := archiveFixture(t, dest, "our-uuid")

	d.runArchive(cfg, job)

	errText, ok := archiveResultFor(t, d, "nightly")
	if ok {
		t.Fatal("a snapshot was written to a destination that is not mounted")
	}
	if errText != "target offline" {
		t.Errorf("an absent destination was reported as %q", errText)
	}
	if _, err := os.Stat(dest); !os.IsNotExist(err) {
		t.Errorf("the mount point was brought into existence: %v", err)
	}
}

// And the healthy case still works, or the three refusals above would be
// indistinguishable from a snapshot writer that never writes anything.
func TestASnapshotToRecognisedStorageIsWritten(t *testing.T) {
	dest := t.TempDir()
	if err := localmirror.WriteMarkerAt(dest, "our-uuid", "workstation"); err != nil {
		t.Fatal(err)
	}
	d, cfg, job, logs := archiveFixture(t, dest, "our-uuid")

	d.runArchive(cfg, job)

	if errText, ok := archiveResultFor(t, d, "nightly"); !ok {
		t.Fatalf("a snapshot to the right drive failed: %s\n%s", errText, logs.String())
	}
	// The zip lands at backup-maker-archives/<machine>/<job>/<name>.zip, so the
	// assertion walks rather than assuming a depth.
	var zips []string
	_ = filepath.WalkDir(dest, func(p string, e fs.DirEntry, err error) error {
		if err == nil && !e.IsDir() && filepath.Ext(e.Name()) == ".zip" {
			zips = append(zips, p)
		}
		return nil
	})
	if len(zips) == 0 {
		t.Error("no snapshot was written to a recognised drive")
	}
	for _, z := range zips {
		if info, err := os.Stat(z); err != nil || info.Size() == 0 {
			t.Errorf("snapshot %s is empty or unreadable: %v", z, err)
		}
	}
}
