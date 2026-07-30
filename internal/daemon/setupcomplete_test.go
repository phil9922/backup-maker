// SPDX-License-Identifier: MIT

package daemon

import (
	"bytes"
	"context"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/phil9922/backup-maker/internal/config"
	"github.com/phil9922/backup-maker/internal/localmirror"
)

// setupDaemon builds a daemon whose destinations are a temp directory, so
// applyConfig can run end to end without any real storage.
func setupDaemon(t *testing.T, cfg *config.Config) *daemon {
	t.Helper()
	root := t.TempDir()
	state := &config.State{}
	if err := state.Save(); err != nil {
		t.Fatalf("seeding state: %v", err)
	}
	d := &daemon{
		log:   slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)),
		state: state,
		cfg:   cfg,
		marks: newSyncMarks(state),
	}
	d.newBackend = func(config.Target, map[string]string) (localmirror.Backend, time.Duration, bool, error) {
		return localmirror.NewLocalFS(root), 5 * time.Second, false, nil
	}
	return d
}

// configured is a machine that protects one folder onto one drive — the shape
// `backup-maker add-folder` + `add-target drive` leaves behind, which is how
// Phil's machine was built and the case that had no persisted flag.
func configuredCfg(t *testing.T) *config.Config {
	t.Helper()
	return &config.Config{
		General: config.General{MachineName: "my-laptop"},
		Folders: []config.Folder{{ID: "f1", Path: t.TempDir(), Label: "development"}},
		Targets: []config.Target{{Type: "drive", Name: "laptopcard", Path: t.TempDir()}},
	}
}

// THE BUG THIS EXISTS FOR, and the one the user actually hit. A machine set up
// from the CLI never had state.SetupComplete written — only the browser wizard
// and adopt ever wrote it — so "is this set up" was answered purely from the
// live config. Clicking "Stop protecting" on the last folder made that answer
// false, and the dashboard threw the first-run wizard over a working install.
func TestRemovingTheLastFolderDoesNotReopenTheWizard(t *testing.T) {
	isolateState(t)
	cfg := configuredCfg(t)
	d := setupDaemon(t, cfg)

	d.applyConfig(context.Background(), cfg)

	// Now the folder goes, exactly as the dashboard's DELETE does.
	empty := &config.Config{
		General: cfg.General,
		Targets: cfg.Targets,
	}
	d.cfg = empty
	d.applyConfig(context.Background(), empty)

	// setupDone is what the status collector reads for `flagged`, and it is the
	// only thing standing between an emptied config and the first-run wizard.
	if !d.setupDone() {
		t.Error("removing the last folder reported a fresh install; the dashboard would cover the page with the first-run wizard")
	}
	if empty.Configured() {
		t.Error("a config with no folders reports itself as configured; the flag is then the only thing holding the wizard back, which is not what this test is proving")
	}
}

// The flag is persisted, not merely held in memory: a daemon restart must not
// re-open the wizard either.
func TestApplyConfigRecordsThatACLICreatedInstallIsSetUp(t *testing.T) {
	isolateState(t)
	cfg := configuredCfg(t)
	d := setupDaemon(t, cfg)

	// RECEIVING STARTS A REAL CHILD PROCESS, and it has to be stopped before the
	// directory it was unpacked into is removed. Nothing cancelled
	// context.Background(), so the engine ran for the rest of the test binary:
	// on Linux the directory can be deleted from under a running executable, so
	// the leak was invisible, while Windows refuses to unlink one and said so.
	// Cleanups run in reverse order of registration, so this cancel happens
	// before the temp directories go.
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(func() {
		cancel()
		// The supervisor stops asynchronously and there is nothing to join, so
		// give the process a moment to go before its binary is deleted.
		time.Sleep(250 * time.Millisecond)
	})

	d.applyConfig(ctx, cfg)

	onDisk, err := config.LoadState()
	if err != nil {
		t.Fatal(err)
	}
	if !onDisk.SetupComplete {
		t.Error("a configured machine did not record that it was set up, so the flag dies with the process")
	}
}

// The other side of the promise: a genuinely fresh install is still owed the
// wizard, and nothing is written that would take it away.
func TestApplyConfigLeavesAFreshInstallOwedTheWizard(t *testing.T) {
	isolateState(t)
	cfg := &config.Config{General: config.General{MachineName: "brand-new"}}
	d := setupDaemon(t, cfg)

	d.applyConfig(context.Background(), cfg)

	onDisk, err := config.LoadState()
	if err != nil {
		t.Fatal(err)
	}
	if onDisk.SetupComplete {
		t.Error("an empty install recorded itself as set up; the first-run wizard would never appear")
	}
}

// A folder with nowhere to send it is not a backup, so it is still a first run.
func TestAFolderWithNoDestinationIsStillAFirstRun(t *testing.T) {
	isolateState(t)
	cfg := &config.Config{
		General: config.General{MachineName: "half-done"},
		Folders: []config.Folder{{ID: "f1", Path: t.TempDir(), Label: "development"}},
	}
	d := setupDaemon(t, cfg)

	d.applyConfig(context.Background(), cfg)

	onDisk, err := config.LoadState()
	if err != nil {
		t.Fatal(err)
	}
	if onDisk.SetupComplete {
		t.Error("a folder with no destination counted as a finished setup")
	}
}

// A machine that only RECEIVES backups is set up too. It has no folders and no
// targets by design, and the wizard covers the whole page — which used to put
// the "approve this machine" panel permanently out of reach on the one computer
// that needs it.
func TestApplyConfigRecordsAReceiveOnlyMachineAsSetUp(t *testing.T) {
	isolateState(t)
	cfg := &config.Config{
		General: config.General{MachineName: "attic-pi"},
		Receive: config.Receive{Enabled: true, Root: t.TempDir()},
	}
	d := setupDaemon(t, cfg)

	d.applyConfig(context.Background(), cfg)

	onDisk, err := config.LoadState()
	if err != nil {
		t.Fatal(err)
	}
	if !onDisk.SetupComplete {
		t.Error("a receive-only machine was not recorded as set up")
	}
}

// The steady state must not re-read state.json on every watcher tick just to
// learn something it already knows. Guarding on the in-memory flag is what
// keeps a 2-second loop from becoming a 2-second file read, so prove the guard
// is actually in front of the write.
func TestASetUpMachineDoesNotRewriteTheFlagOnEveryApply(t *testing.T) {
	isolateState(t)
	cfg := configuredCfg(t)
	d := setupDaemon(t, cfg)
	d.applyConfig(context.Background(), cfg)

	path, err := config.StatePath()
	if err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	before := fi.ModTime()
	// Coarse filesystem timestamps would hide a rewrite that happened within
	// the same tick, so give the clock room to move.
	time.Sleep(20 * time.Millisecond)

	for range 5 {
		d.applyConfig(context.Background(), cfg)
	}

	fi, err = os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if after := fi.ModTime(); !after.Equal(before) {
		t.Errorf("state.json was rewritten by a later apply (%v -> %v); the already-set-up guard is not in front of the write", before, after)
	}
}

// NOT TESTED HERE: that the self-heal's position in applyConfig still leaves
// destinations prepared. recognition_test.go's
// TestFirstRunDestinationIsMarkedAndWritten already drives that path properly —
// with the marker established and the UUID recorded, which is what a manifest
// write requires — and applyconfig_test.go covers the lock discipline around
// it. A second, thinner copy here would only re-prove it badly.
