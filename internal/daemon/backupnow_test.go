// SPDX-License-Identifier: MIT

package daemon

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/phil9922/backup-maker/internal/config"
)

// snapshotDaemon has one schedule that is ready to run in every respect except
// that its destination does not exist — so runArchive, if it ever gets that
// far, fails immediately and touches no storage.
func snapshotDaemon(t *testing.T) *daemon {
	t.Helper()
	isolateState(t)
	cfg := config.New()
	cfg.General.MachineName = "my-laptop"
	cfg.Folders = []config.Folder{{ID: "kqz3d-8xh2p", Label: "photos", Path: t.TempDir()}}
	cfg.Archives = []config.Archive{{
		Name: "nightly", Every: "daily", Target: "no-such-target",
		Folders: []string{"kqz3d-8xh2p"}, Keep: 3,
	}}
	return &daemon{
		log:   slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)),
		cfg:   cfg,
		state: &config.State{ArchivePasswords: map[string]string{"nightly": "hunter2"}},
	}
}

// THE GUARANTEE. One snapshot schedule can never be writing two snapshots at
// once, however the second run was asked for.
//
// Both runs derive the same temp file from the job's name and the second it
// started, so each would be writing into the other's zip — and whichever
// finished first would rename or delete the file the other was still filling.
// The dashboard's "running" flag used to be exactly that, a flag: nothing in
// the program stopped a second run, because until "Back up now" existed the
// scheduler was the only caller and it ran jobs one at a time.
func TestAManualSnapshotCannotRunAScheduleThatIsAlreadyRunning(t *testing.T) {
	d := snapshotDaemon(t)

	// As the scheduler looks while it is part-way through this job's zip.
	if !d.claimArchiveRun("nightly") {
		t.Fatal("could not claim a schedule nothing is running")
	}

	_, err := d.backUpArchiveNow("nightly")
	if err == nil {
		t.Fatal("a second run of the same schedule was started beside the first")
	}
	if !strings.Contains(err.Error(), "already writing a snapshot") {
		t.Errorf("the refusal does not say why: %v", err)
	}

	// And it is a refusal for the duration only, not a schedule taken out of
	// service: once the run ends, the button works again.
	d.releaseArchiveRun("nightly")
	if _, err := d.backUpArchiveNow("nightly"); err != nil {
		t.Errorf("the schedule stayed blocked after its run finished: %v", err)
	}
	// That one really did start, on its own goroutine. Waited out so it cannot
	// outlive the test — it fails at once, the destination not existing.
	waitForArchiveToFinish(t, d, "nightly")
}

func waitForArchiveToFinish(t *testing.T, d *daemon, name string) {
	t.Helper()
	for i := 0; i < 500; i++ {
		d.mu.Lock()
		_, running := d.archiveRunning[name]
		d.mu.Unlock()
		if !running {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("the run of %q never finished", name)
}

// The other half of the same guarantee, from the other side: the scheduler must
// respect a claim a manual run holds, rather than starting the job beside it 30
// seconds later.
func TestTheSchedulerSkipsAScheduleThatIsAlreadyRunning(t *testing.T) {
	d := snapshotDaemon(t)
	if !d.claimArchiveRun("nightly") { // as "Back up now" leaves it
		t.Fatal("could not claim a schedule nothing is running")
	}

	d.runDueArchives() // the job is due: never run, not paused, 30s tick

	// runArchive records a result for every job it touches, including the ones
	// it fails — so an entry here means it ran the job anyway.
	d.mu.Lock()
	_, ran := d.archiveResults["nightly"]
	d.mu.Unlock()
	if ran {
		t.Error("the scheduler started a job that a manual run was already writing")
	}
}

// Refused BEFORE the press has any effect, with the reason. A button that
// reports success and then produces nothing is the failure this project has
// shipped twice, and every one of these is a state the panel greys the button
// out for — checked again here because a page can be minutes old.
func TestBackUpNowRefusesASnapshotThatCannotRun(t *testing.T) {
	cases := []struct {
		name    string
		break_  func(d *daemon)
		wantSay string
	}{
		{"paused", func(d *daemon) { d.cfg.Archives[0].Paused = true }, "paused"},
		{"no stored password", func(d *daemon) { d.state.ArchivePasswords = nil }, "no stored password"},
		{"covers nothing", func(d *daemon) { d.cfg.Folders = nil }, "covers no folder"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			d := snapshotDaemon(t)
			c.break_(d)
			_, err := d.backUpArchiveNow("nightly")
			if err == nil {
				t.Fatalf("a snapshot that cannot run reported that it had started")
			}
			if !strings.Contains(err.Error(), c.wantSay) {
				t.Errorf("refusal %q does not say %q", err, c.wantSay)
			}
			// Nothing was claimed, so the schedule is not left wedged.
			d.mu.Lock()
			_, claimed := d.archiveRunning["nightly"]
			d.mu.Unlock()
			if claimed {
				t.Error("a refused run left the schedule claimed; it would never run again")
			}
		})
	}
	// And a name nothing answers to.
	d := snapshotDaemon(t)
	if _, err := d.backUpArchiveNow("no-such-schedule"); err == nil {
		t.Error("a schedule that does not exist reported that it had started")
	}
}

// The mirror side of the same rule: a pair with no engine has no pass to run,
// and saying so beats a button that appears to work.
func TestBackUpNowRefusesAPairThatIsNotBeingMirrored(t *testing.T) {
	d := snapshotDaemon(t)

	_, err := d.backUpFolderNow("kqz3d-8xh2p", "laptopcard")
	if err == nil {
		t.Fatal("a folder that is copied nowhere reported that a backup had started")
	}
	// Named in the user's own words, not by an internal id.
	if !strings.Contains(err.Error(), "photos") || !strings.Contains(err.Error(), "laptopcard") {
		t.Errorf("the refusal does not say which pair: %v", err)
	}

	if _, err := d.backUpFolderNow("kqz3d-8xh2p", ""); err == nil {
		t.Error("a request with no destination was accepted")
	}
}
