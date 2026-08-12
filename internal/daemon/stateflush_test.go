// SPDX-License-Identifier: MIT

package daemon

import (
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/phil9922/backup-maker/internal/config"
	"github.com/phil9922/backup-maker/internal/setup"
	"github.com/phil9922/backup-maker/internal/testpath"
)

// renamableSetup writes a config and a state file describing one share
// destination that has been backed up to, and returns a daemon holding that
// state the way a running one does.
func renamableSetup(t *testing.T, syncedAt time.Time) *daemon {
	t.Helper()
	isolateState(t)
	cfg := config.New()
	cfg.General.MachineName = "my-laptop"
	cfg.Folders = []config.Folder{{ID: "fold1", Path: testpath.Abs("/home/alex/code"), Label: "code"}}
	cfg.Targets = []config.Target{
		{Type: "share", Name: "backups", URL: "//pi/backups", Username: "alex", Folders: []string{}},
	}
	if err := cfg.Save(); err != nil {
		t.Fatal(err)
	}
	seed := &config.State{
		DriveTargetUUIDs: map[string]string{"backups": "uuid-pi"},
		ShareCredentials: map[string]string{"backups": "the-password"},
		MirrorLastSync:   map[string]map[string]time.Time{"fold1": {"backups": syncedAt}},
		MirrorScanState:  map[string]map[string]config.ScanMark{"fold1": {"backups": {TargetUUID: "uuid-pi"}}},
		BytesCopiedTotal: 1000,
		FilesCopiedTotal: 2,
		CountingSince:    syncedAt.Add(-24 * time.Hour),
	}
	if err := seed.Save(); err != nil {
		t.Fatal(err)
	}
	state, err := config.LoadState()
	if err != nil {
		t.Fatal(err)
	}
	d := &daemon{state: state, cfg: cfg}
	d.marks = newSyncMarks(state)
	d.tally = newTally(state, d.saveState)
	return d
}

// THE GUARANTEE, and the outage that bought it. Renaming a destination writes
// state.json; the daemon's odometer flush writes state.json every thirty
// seconds from its own long-held copy. On 2026-08-11 the second rename of the
// afternoon was overwritten by the flush that landed a moment later:
// config.toml said "crucial-rasp_pi" while state.json still filed the marker
// UUID and the SMB password under the old name. The daemon logged "target has
// no recorded UUID; re-add it", could not log in to the share, and that
// destination backed up nothing for forty minutes without a word on the
// dashboard.
//
// So: a rename, then the flush that used to undo it, and everything the rename
// moved is still there afterwards — while the counter the flush exists to write
// has NOT wound backwards to the figure the rename's save copied out of the
// file.
func TestARenameSurvivesTheDaemonsNextStateFlush(t *testing.T) {
	synced := time.Date(2026, 8, 11, 9, 0, 0, 0, time.UTC)
	d := renamableSetup(t, synced)

	// Work done since the last flush: this is what makes the flush fire, and
	// what must not be rolled back to the file's figure.
	d.countCopied(4096)

	if err := setup.RenameTarget("backups", "pi-drive1"); err != nil {
		t.Fatalf("renaming: %v", err)
	}
	// The flush that lands a moment later, from a State that still knows the
	// destination by its old name.
	d.tally.flush()

	after, err := config.LoadState()
	if err != nil {
		t.Fatal(err)
	}
	if after.DriveTargetUUIDs["pi-drive1"] != "uuid-pi" {
		t.Errorf("the flush undid the recorded UUID: state.json now holds %v — the daemon would say to re-add the destination",
			after.DriveTargetUUIDs)
	}
	if after.ShareCredentials["pi-drive1"] != "the-password" {
		t.Errorf("the flush undid the share password: state.json now holds the keys %v — that destination cannot log in",
			keysOf(after.ShareCredentials))
	}
	if _, stillThere := after.DriveTargetUUIDs["backups"]; stillThere {
		t.Error("the old name came back in drive_target_uuids")
	}
	if _, stillThere := after.ShareCredentials["backups"]; stillThere {
		t.Error("the old name came back in share_credentials")
	}
	if after.BytesCopiedTotal != 5096 || after.FilesCopiedTotal != 3 {
		t.Errorf("the odometer reads %dB / %d files, want 5096B / 3: memory must still win over the file for the counters",
			after.BytesCopiedTotal, after.FilesCopiedTotal)
	}

	// And the daemon is now holding what was actually written, because the
	// share password it hands the mirror engines comes out of that copy.
	if d.shareCredentials()["pi-drive1"] != "the-password" {
		t.Error("the daemon is still holding the destination under its old name; the mirror engine would get no password")
	}

}

// The other half of a rename, and the reason saveState still writes the sync
// marks from memory rather than from the file: memory has to keep winning for a
// clock, or a flush would wind one backwards to whatever the last setup command
// happened to copy out. The renamed pairs reach memory through syncMarks.fill on
// the config watcher's next pass — see applyConfig — and this is what proves the
// two halves still meet.
func TestARenamedDestinationsClocksReachTheStateFile(t *testing.T) {
	synced := time.Date(2026, 8, 11, 9, 0, 0, 0, time.UTC)
	d := renamableSetup(t, synced)

	if err := setup.RenameTarget("backups", "pi-drive1"); err != nil {
		t.Fatalf("renaming: %v", err)
	}
	// What applyConfig does when the watcher notices config.toml changed.
	fresh, err := config.LoadState()
	if err != nil {
		t.Fatal(err)
	}
	if !d.marks.fill(fresh) {
		t.Fatal("the renamed clocks were not there to be picked up")
	}
	d.tally.touch()
	d.tally.flush()

	after, err := config.LoadState()
	if err != nil {
		t.Fatal(err)
	}
	if !after.MirrorLastSync["fold1"]["pi-drive1"].Equal(synced) {
		t.Errorf("the last-synced clock did not survive: %v — the destination would read as never synced, so a drive nobody has plugged in for months looks merely offline",
			after.MirrorLastSync)
	}
	if after.MirrorScanState["fold1"]["pi-drive1"].TargetUUID != "uuid-pi" {
		t.Errorf("the last-pass record did not survive: %v", after.MirrorScanState)
	}
}

// THE GUARANTEE: no amount of flushing can drop a destination's password or its
// recorded UUID. Losing a share credential is what took a destination offline,
// and it is the one loss the user cannot see and cannot recover from without
// remembering a password they were told they need not.
//
// A setup-style writer adding destinations one at a time, against a daemon
// flushing on its own goroutine as fast as it can — which is the in-process
// half of the race, since the dashboard's actions run inside the daemon.
func TestAConcurrentFlushCannotDropAShareCredential(t *testing.T) {
	d := renamableSetup(t, time.Date(2026, 8, 11, 9, 0, 0, 0, time.UTC))

	const added = 20
	stop := make(chan struct{})
	var flushing sync.WaitGroup
	flushing.Add(1)
	go func() {
		defer flushing.Done()
		for {
			select {
			case <-stop:
				return
			default:
			}
			d.countCopied(100)
			d.tally.flush()
		}
	}()

	for i := range added {
		name := fmt.Sprintf("dest-%d", i)
		if _, err := config.UpdateState(func(s *config.State) error {
			if s.ShareCredentials == nil {
				s.ShareCredentials = map[string]string{}
			}
			if s.DriveTargetUUIDs == nil {
				s.DriveTargetUUIDs = map[string]string{}
			}
			s.ShareCredentials[name] = "password-" + name
			s.DriveTargetUUIDs[name] = "uuid-" + name
			return nil
		}); err != nil {
			t.Fatalf("adding %s: %v", name, err)
		}
	}
	close(stop)
	flushing.Wait()
	d.tally.flush() // whatever the last loop counted

	after, err := config.LoadState()
	if err != nil {
		t.Fatal(err)
	}
	for i := range added {
		name := fmt.Sprintf("dest-%d", i)
		if after.ShareCredentials[name] != "password-"+name {
			t.Errorf("%s lost its share password to a flush: state.json holds %v", name, keysOf(after.ShareCredentials))
		}
		if after.DriveTargetUUIDs[name] != "uuid-"+name {
			t.Errorf("%s lost its recorded UUID to a flush: state.json holds %v", name, keysOf(after.DriveTargetUUIDs))
		}
	}
	if after.ShareCredentials["backups"] != "the-password" {
		t.Error("the destination that was there all along lost its password")
	}
	// The flushes are still doing their own job while all that goes on.
	if after.BytesCopiedTotal <= 1000 {
		t.Errorf("the odometer did not move: %d", after.BytesCopiedTotal)
	}
}

func keysOf(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
