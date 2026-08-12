// SPDX-License-Identifier: MIT

package daemon

import (
	"bytes"
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/phil9922/backup-maker/internal/config"
	"github.com/phil9922/backup-maker/internal/localmirror"
)

// pausedMirrorDaemon is one folder mirrored to one attached drive, ready for
// applyConfig to start an engine for it. The source holds one file and the
// destination already holds a copy of an older one, so both halves of the
// promise below can be checked: nothing new arrives, and nothing already there
// is removed.
func pausedMirrorDaemon(t *testing.T) (d *daemon, cfg *config.Config, src, dest string) {
	t.Helper()
	isolateState(t)
	base := t.TempDir()
	src = filepath.Join(base, "photos")
	dest = filepath.Join(base, "card")
	for _, dir := range []string{src, dest} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(src, "holiday.jpg"), []byte("a photograph"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := localmirror.WriteMarkerAt(dest, "card-uuid", "my-laptop"); err != nil {
		t.Fatal(err)
	}
	state := &config.State{DriveTargetUUIDs: map[string]string{"sdcard": "card-uuid"}}
	if err := state.Save(); err != nil {
		t.Fatalf("seeding state: %v", err)
	}
	cfg = &config.Config{
		General:  config.General{MachineName: "my-laptop"},
		Defaults: config.Defaults{StaleAfterDays: 7},
		Folders:  []config.Folder{{ID: "kqz3d-8xh2p", Label: "photos", Path: src}},
		Targets:  []config.Target{{Type: "drive", Name: "sdcard", Path: dest}},
	}
	d = &daemon{
		log:   slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)),
		state: state,
		cfg:   cfg,
		marks: newSyncMarks(state),
	}
	return d, cfg, src, dest
}

// destPath is where this machine's copy of the folder lands on the destination.
func destPath(dest, file string) string {
	return filepath.Join(dest, filepath.FromSlash(config.DestRoot("my-laptop", "photos")), file)
}

// stopEngines cancels the engine set and waits for the goroutines to leave the
// destination alone. Registered as a cleanup AFTER the temp directories are
// made, so it runs BEFORE they are deleted (cleanups run last-in-first-out) —
// otherwise a pass still in flight writes into a directory RemoveAll is walking,
// and the test fails on cleanup rather than on anything it asserts.
func stopEngines(t *testing.T, d *daemon, cancel context.CancelFunc) {
	t.Helper()
	t.Cleanup(func() {
		cancel()
		for range 500 {
			busy := false
			for _, e := range d.currentEngines() {
				busy = busy || e.Busy()
			}
			if !busy {
				// A moment for the run loop to notice the cancellation and stop,
				// so nothing is mid-write when the directories go.
				time.Sleep(20 * time.Millisecond)
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
	})
}

func waitForFile(t *testing.T, path string) {
	t.Helper()
	for range 400 {
		if _, err := os.Stat(path); err == nil {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("%s never appeared; this test needs a mirror that actually copies", path)
}

// THE GUARANTEE: pausing copies nothing and DELETES NOTHING.
//
// A pause is a change of intent, like removing a folder or a destination — and
// this project's first rule is that a change of intent never removes a backup.
// So the copy already on the destination has to be sitting there untouched
// afterwards, and a file saved while the pair is paused must not travel.
//
// Run against a real engine and a real destination directory, because "no engine
// was started" is a statement about our code and "the files are still there" is
// the statement somebody's photographs depend on.
func TestPausingAMirrorCopiesNothingAndDeletesNothing(t *testing.T) {
	d, cfg, src, dest := pausedMirrorDaemon(t)
	ctx, cancel := context.WithCancel(context.Background())
	stopEngines(t, d, cancel)

	// Running first, so the destination really holds a backup to be preserved —
	// and so a failure to copy later cannot be mistaken for a harness that never
	// copied anything.
	d.applyConfig(ctx, cfg)
	waitForFile(t, destPath(dest, "holiday.jpg"))

	// Now paused, exactly as setup.SetMirrorPaused would leave the config.
	paused := *cfg
	paused.Folders = []config.Folder{{
		ID: "kqz3d-8xh2p", Label: "photos", Path: src, PausedTargets: []string{"sdcard"},
	}}
	d.applyConfig(ctx, &paused)

	if engines := d.currentEngines(); len(engines) != 0 {
		t.Errorf("a paused pair still has %d mirror engine(s) running", len(engines))
	}
	// A file saved while it is paused must stay where it is.
	if err := os.WriteFile(filepath.Join(src, "new.jpg"), []byte("saved while paused"), 0o644); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(destPath(dest, "new.jpg")); err == nil {
			t.Fatal("a file saved while the mirror was paused was copied to the destination anyway")
		}
		time.Sleep(10 * time.Millisecond)
	}
	// And the backup that was already there is still there, byte for byte.
	got, err := os.ReadFile(destPath(dest, "holiday.jpg"))
	if err != nil {
		t.Fatalf("pausing removed a file from the destination: %v", err)
	}
	if string(got) != "a photograph" {
		t.Errorf("the backed-up file was rewritten while paused: %q", got)
	}
	// Nothing was touched in the SOURCE either — the rule that outranks
	// everything else in this repository.
	if _, err := os.Stat(filepath.Join(src, "holiday.jpg")); err != nil {
		t.Fatalf("the source folder lost a file: %v", err)
	}
}

// THE GUARANTEE: resuming picks up where it left off.
//
// The pair's last-synced clock and its scan marks live in state.json, keyed by
// folder and destination, and prune() drops the pairs the config no longer has.
// A pause that made the pair look gone would take those with it — so the resumed
// mirror would report "never synced" on a destination holding a complete backup,
// and would recopy everything to find out it was already there.
func TestResumingAMirrorDoesNotResetItsSyncClocks(t *testing.T) {
	d, cfg, src, dest := pausedMirrorDaemon(t)
	ctx, cancel := context.WithCancel(context.Background())
	stopEngines(t, d, cancel)

	d.applyConfig(ctx, cfg)
	waitForFile(t, destPath(dest, "holiday.jpg"))

	var synced time.Time
	for range 400 {
		if synced = d.marks.lastSync("kqz3d-8xh2p", "sdcard"); !synced.IsZero() {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if synced.IsZero() {
		t.Fatal("the pair never recorded a completed sync; this test has nothing to preserve")
	}
	before := d.marks.scanMark("kqz3d-8xh2p", "sdcard", "card-uuid")

	paused := *cfg
	paused.Folders = []config.Folder{{
		ID: "kqz3d-8xh2p", Label: "photos", Path: src, PausedTargets: []string{"sdcard"},
	}}
	d.applyConfig(ctx, &paused)

	if got := d.marks.lastSync("kqz3d-8xh2p", "sdcard"); !got.Equal(synced) {
		t.Errorf("the pair's last-synced clock changed over a pause: %v, was %v", got, synced)
	}
	if got := d.marks.scanMark("kqz3d-8xh2p", "sdcard", "card-uuid"); got != before {
		t.Errorf("the pair's scan mark was lost over a pause: %+v, was %+v", got, before)
	}
	// And the row the dashboard draws says when it was last backed up, rather
	// than "never" — which is what a lost clock would produce.
	if got := d.lastSyncMark("kqz3d-8xh2p", "sdcard"); !got.Equal(synced) {
		t.Errorf("the status collector is told %v, want %v", got, synced)
	}

	// Resumed: the engine comes back, seeded from the clock that survived.
	d.applyConfig(ctx, cfg)
	engines := d.currentEngines()
	if len(engines) != 1 {
		t.Fatalf("resuming left %d engines, want 1", len(engines))
	}
	if got := engines[0].Status().LastSync; !got.Equal(synced) {
		t.Errorf("the resumed engine started from %v, want the clock it had: %v", got, synced)
	}
}

// "Back up now" on a paused pair must say why and what to press, in the same
// shape as the snapshot side's refusal. Without this it falls through to
// "nothing is copying this continuously" — true, and useless.
func TestBackUpNowRefusesAPausedPair(t *testing.T) {
	d, cfg, _, _ := pausedMirrorDaemon(t)
	cfg.Folders[0].PausedTargets = []string{"sdcard"}

	_, err := d.backUpFolderNow("kqz3d-8xh2p", "sdcard")
	if err == nil {
		t.Fatal("a paused pair reported that a backup had started")
	}
	if !strings.Contains(err.Error(), "paused") || !strings.Contains(strings.ToLower(err.Error()), "resume") {
		t.Errorf("the refusal does not say it is paused and to resume it: %v", err)
	}
	// Named in the user's own words, like every other refusal here.
	if !strings.Contains(err.Error(), "photos") || !strings.Contains(err.Error(), "sdcard") {
		t.Errorf("the refusal does not say which pair: %v", err)
	}
}
