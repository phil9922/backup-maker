// SPDX-License-Identifier: MIT

package daemon

import (
	"context"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/phil9922/backup-maker/internal/config"
	"github.com/phil9922/backup-maker/internal/localmirror"
	"github.com/phil9922/backup-maker/internal/status"
	"github.com/phil9922/backup-maker/internal/syncthing"
)

// markedDaemon is a daemon with the two pieces of batched state wired up the
// way Run wires them, and nothing else.
func markedDaemon(t *testing.T, s *config.State) *daemon {
	t.Helper()
	d := &daemon{log: slog.New(slog.DiscardHandler), state: s}
	d.marks = newSyncMarks(s)
	d.tally = newTally(s, d.saveState)
	return d
}

// THE HEADLINE: how long a destination has been missing must survive the daemon
// being stopped and started, because that is precisely when the answer matters.
// Written through the real state.json, since that round trip is what used to be
// absent altogether.
func TestSyncMarksSurviveARestart(t *testing.T) {
	isolateState(t)
	synced := time.Now().Add(-30 * 24 * time.Hour).Truncate(time.Millisecond)

	first := markedDaemon(t, &config.State{})
	first.syncRecorder("photos", "sdcard")(synced)
	first.syncRecorder("code", "nas")(synced.Add(time.Hour))
	first.tally.flush()

	// A new process reads what the old one left behind.
	reloaded, err := config.LoadState()
	if err != nil {
		t.Fatalf("loading state: %v", err)
	}
	if got := reloaded.MirrorLastSync["photos"]["sdcard"]; !got.Equal(synced) {
		t.Fatalf("state.json holds %v for photos → sdcard, want %v", got, synced)
	}

	second := markedDaemon(t, reloaded)
	if got := second.marks.lastSync("photos", "sdcard"); !got.Equal(synced) {
		t.Fatalf("after the restart the clock reads %v, want %v", got, synced)
	}
	if got := second.marks.lastSync("code", "nas"); !got.Equal(synced.Add(time.Hour)) {
		t.Errorf("the second destination's clock was lost: %v", got)
	}

	// And the new process's own syncs carry on from there.
	fresh := time.Now().Truncate(time.Millisecond)
	second.syncRecorder("photos", "sdcard")(fresh)
	second.tally.flush()
	after, err := config.LoadState()
	if err != nil {
		t.Fatalf("loading state: %v", err)
	}
	if got := after.MirrorLastSync["photos"]["sdcard"]; !got.Equal(fresh) {
		t.Errorf("the second run persisted %v, want %v", got, fresh)
	}
	if got := after.MirrorLastSync["code"]["nas"]; !got.Equal(synced.Add(time.Hour)) {
		t.Errorf("the second run dropped a destination it never synced: %v", got)
	}
	// The odometer rode the same write and is intact.
	if after.CountingSince.IsZero() {
		t.Error("writing the sync marks lost the odometer's start date")
	}
}

// A pair that has never synced has no mark, and must not acquire one by being
// asked about: an absent timestamp is what keeps a brand-new destination reading
// as offline rather than as long overdue.
func TestSyncMarksNeverSyncedStaysAbsent(t *testing.T) {
	isolateState(t)
	d := markedDaemon(t, &config.State{})

	if got := d.marks.lastSync("photos", "sdcard"); !got.IsZero() {
		t.Fatalf("a pair that never synced reports %v", got)
	}
	d.tally.flush()
	reloaded, err := config.LoadState()
	if err != nil {
		t.Fatalf("loading state: %v", err)
	}
	if len(reloaded.MirrorLastSync) != 0 {
		t.Errorf("state.json invented marks: %v", reloaded.MirrorLastSync)
	}

	// A zero time read back from a hand-edited state.json is "never" too, not a
	// destination last synced in the year 1.
	seeded := newSyncMarks(&config.State{
		MirrorLastSync: map[string]map[string]time.Time{"photos": {"sdcard": {}}},
	})
	if got := seeded.lastSync("photos", "sdcard"); !got.IsZero() {
		t.Errorf("a zero mark was loaded as %v", got)
	}
	if got := seeded.snapshot(); got != nil {
		t.Errorf("a zero mark was carried back into state.json: %v", got)
	}
}

// Syncing must not rewrite state.json per pass: a folder that changes often
// syncs every few seconds, and this file is not a log.
func TestSyncMarksAreWrittenInBatches(t *testing.T) {
	writes := 0
	d := &daemon{state: &config.State{}}
	d.marks = newSyncMarks(d.state)
	d.tally = newTally(&config.State{CountingSince: time.Now()},
		func(uint64, uint64, time.Time) error { writes++; return nil })

	record := d.syncRecorder("photos", "sdcard")
	at := time.Now()
	for i := range 500 {
		record(at.Add(time.Duration(i) * time.Second))
	}
	if writes != 0 {
		t.Fatalf("500 sync passes caused %d writes to state.json before any flush", writes)
	}
	d.tally.flush()
	if writes != 1 {
		t.Fatalf("flush wrote %d times, want 1", writes)
	}
	// Nothing new to say, so nothing is written.
	d.tally.flush()
	if writes != 1 {
		t.Fatalf("an idle flush rewrote state.json (%d writes)", writes)
	}
	// The last pass is the one that survives, not the first.
	if got := d.marks.lastSync("photos", "sdcard"); !got.Equal(at.Add(499 * time.Second)) {
		t.Errorf("the clock reads %v after 500 passes, want the latest", got)
	}
}

// A folder or destination that has been removed from the config must not leave
// its clock behind: state.json would grow for ever, and a target name that came
// back later would inherit a stale time it never earned.
func TestSyncMarksPruneWhatTheConfigNoLongerHas(t *testing.T) {
	now := time.Now()
	m := newSyncMarks(&config.State{MirrorLastSync: map[string]map[string]time.Time{
		"photos": {"sdcard": now, "nas": now, "gone-target": now},
		"code":   {"sdcard": now},
		"gone":   {"sdcard": now},
	}})
	cfg := &config.Config{
		Folders: []config.Folder{{ID: "photos"}, {ID: "code"}},
		Targets: []config.Target{
			{Type: "drive", Name: "sdcard", Path: "/mnt/sd"}, // every folder
			{Type: "share", Name: "nas", URL: "//nas/backups", Folders: []string{"photos"}},
		},
	}

	if !m.prune(cfg) {
		t.Fatal("prune reported nothing to drop, so the shorter file is never written")
	}
	want := map[string]map[string]bool{
		"photos": {"sdcard": true, "nas": true},
		"code":   {"sdcard": true},
	}
	got := m.snapshot()
	if len(got) != len(want) {
		t.Fatalf("after pruning: %v, want folders %v", got, want)
	}
	for folder, targets := range want {
		if len(got[folder]) != len(targets) {
			t.Errorf("folder %q kept %v, want %v", folder, got[folder], targets)
		}
		for target := range targets {
			if got[folder][target].IsZero() {
				t.Errorf("folder %q lost its live destination %q", folder, target)
			}
		}
	}

	// Pruning again has nothing to do, so it does not make state.json dirty.
	if m.prune(cfg) {
		t.Error("a second prune claimed it dropped something")
	}

	// A destination that stops mirroring — turned into a paired machine, or
	// switched to snapshots only — has no engine and so no clock either.
	cfg.Targets[0].ArchivesOnly = true
	if !m.prune(cfg) {
		t.Error("an archives-only destination kept its mirror clock")
	}
	if got := m.snapshot(); len(got) != 1 || got["photos"]["nas"].IsZero() {
		t.Errorf("after archives_only: %v, want photos → nas alone", got)
	}
}

// Names come from config.toml, which a user edits by hand: a folder id or a
// destination name may contain anything at all, delimiters included. Nesting the
// map is what makes one destination inheriting another's clock impossible rather
// than merely unlikely.
func TestSyncMarksCannotCollideOnAwkwardNames(t *testing.T) {
	isolateState(t)
	d := markedDaemon(t, &config.State{})
	early := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	late := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)

	// These two pairs join to the same string under any single delimiter.
	d.syncRecorder("photos", "sd:card")(early)
	d.syncRecorder("photos:sd", "card")(late)
	d.tally.flush()

	reloaded, err := config.LoadState()
	if err != nil {
		t.Fatalf("loading state: %v", err)
	}
	m := newSyncMarks(reloaded)
	if got := m.lastSync("photos", "sd:card"); !got.Equal(early) {
		t.Errorf(`"photos" → "sd:card" reads %v, want %v`, got, early)
	}
	if got := m.lastSync("photos:sd", "card"); !got.Equal(late) {
		t.Errorf(`"photos:sd" → "card" reads %v, want %v`, got, late)
	}
}

// THE POINT OF THE WHOLE CHANGE, end to end: the daemon restarts, the seeded
// engine finds its drive still missing, and the user is told — once.
//
// Both halves matter. Before the seed the destination read "offline" for ever
// and this alert could never fire at all; and a reboot must not turn into a
// burst of duplicates, which is what would train the user to dismiss it.
func TestARestartWithAStaleDestinationAlertsOnceNotEveryCycle(t *testing.T) {
	isolateState(t)
	lastSync := time.Now().Add(-30 * 24 * time.Hour)

	// The run before the restart records a sync and stops.
	first := markedDaemon(t, &config.State{})
	first.syncRecorder("photos", "sdcard")(lastSync)
	first.tally.flush()

	// The run after it seeds an engine from state.json, exactly as applyConfig
	// does, and the drive is still not there.
	state, err := config.LoadState()
	if err != nil {
		t.Fatalf("loading state: %v", err)
	}
	d := markedDaemon(t, state)
	d.cfg = &config.Config{
		General:  config.General{MachineName: "workstation"},
		Defaults: config.Defaults{StaleAfterDays: 7},
		Folders:  []config.Folder{{ID: "photos", Label: "Photos", Path: "/home/alex/Photos"}},
		Targets:  []config.Target{{Type: "drive", Name: "sdcard", Path: "/mnt/sd"}},
	}
	e := localmirror.New(localmirror.Options{
		FolderID: "photos", TargetName: "sdcard", TargetType: "drive",
		SourcePath: t.TempDir(), Backend: localmirror.NewLocalFS(filepath.Join(t.TempDir(), "unplugged")),
		MachineName: "workstation", Label: "Photos", UUID: "uuid-1",
		LastSync: d.marks.lastSync("photos", "sdcard"),
		Synced:   d.syncRecorder("photos", "sdcard"),
		Log:      d.log,
	})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	e.Run(ctx)
	d.engines = []*localmirror.Engine{e}

	collector := &status.Collector{
		Cfg:     d.currentCfg,
		Client:  func() *syncthing.Client { return nil },
		Engines: d.currentEngines,
		Totals:  d.totals,
	}
	a, _ := testAlerter(true)
	now := time.Now()

	got := a.pending(collector.Collect(), now)
	if len(got) != 1 {
		t.Fatalf("the first cycle after the restart raised %d alerts, want the stale drive: %s",
			len(got), titles(got))
	}
	if !strings.Contains(got[0].title, "sdcard") {
		t.Errorf("the alert does not name the destination: %+v", got[0])
	}
	if !strings.Contains(got[0].body, "30 days ago") {
		t.Errorf("the alert does not say how long it has been: %+v", got[0])
	}

	// Cycle after cycle, the same known problem stays quiet.
	for i := range 3 {
		if again := a.pending(collector.Collect(), now.Add(time.Duration(i+1)*time.Minute)); len(again) != 0 {
			t.Fatalf("the restart replayed the alert on cycle %d: %s", i+2, titles(again))
		}
	}
}

// THE GUARANTEE: a destination that gets renamed keeps how long it has been
// since each folder reached it.
//
// The half of a rename that lives here, and the one that is invisible until it
// matters. setup.RenameTarget copies the clocks in state.json across to the new
// name — but this process holds them in memory, keyed by the OLD name, and the
// next flush writes memory over the file. Every folder would then read "never
// synced" to a destination that has been backed up for months, so a drive
// nobody has plugged in since would look merely offline instead of overdue.
// Which is the exact fault syncMarks was written to prevent.
func TestARenamedDestinationKeepsItsSyncClocks(t *testing.T) {
	synced := time.Date(2026, 7, 20, 9, 0, 0, 0, time.UTC)
	scan := config.ScanMark{TargetUUID: "uuid-pi"}
	before := &config.State{
		MirrorLastSync:  map[string]map[string]time.Time{"code": {"backups": synced}},
		MirrorScanState: map[string]map[string]config.ScanMark{"code": {"backups": scan}},
	}
	m := newSyncMarks(before)

	// What setup.RenameTarget leaves on disk: the same clocks under both names,
	// the old one about to be dropped.
	renamed := &config.State{
		MirrorLastSync: map[string]map[string]time.Time{
			"code": {"backups": synced, "pi-drive1": synced},
		},
		MirrorScanState: map[string]map[string]config.ScanMark{
			"code": {"backups": scan, "pi-drive1": scan},
		},
	}
	if !m.fill(renamed) {
		t.Fatal("nothing was taken from the state file, so the rename's clocks are lost on the next flush")
	}
	if got := m.lastSync("code", "pi-drive1"); !got.Equal(synced) {
		t.Errorf("the renamed destination's clock is %v, want %v: it would read as never synced", got, synced)
	}
	if got := m.scanMark("code", "pi-drive1", "uuid-pi"); got.TargetUUID != "uuid-pi" {
		t.Error("the renamed destination lost what its last pass learned")
	}

	// And then prune drops the old name, because the config no longer has it.
	cfg := &config.Config{
		Folders: []config.Folder{{ID: "code"}},
		Targets: []config.Target{{Type: "share", Name: "pi-drive1", URL: "//pi/backups/drive1"}},
	}
	if !m.prune(cfg) {
		t.Error("the old name was left behind in state.json")
	}
	if got := m.snapshot(); !got["code"]["backups"].IsZero() {
		t.Error("the old name still has a clock after pruning")
	}
	if got := m.snapshot(); !got["code"]["pi-drive1"].Equal(synced) {
		t.Error("pruning took the new name's clock too")
	}
}

// MEMORY STILL WINS where both know a pair. A state.json written by a setup
// command is behind this process by definition, and adopting its timestamp would
// wind a clock backwards — which on the staleness alert means a destination
// reporting itself as more overdue than it is.
func TestFillNeverWindsAClockBackwards(t *testing.T) {
	newer := time.Date(2026, 7, 30, 12, 0, 0, 0, time.UTC)
	older := newer.Add(-48 * time.Hour)
	m := newSyncMarks(&config.State{MirrorLastSync: map[string]map[string]time.Time{
		"code": {"card": newer},
	}})
	if m.fill(&config.State{MirrorLastSync: map[string]map[string]time.Time{
		"code": {"card": older},
	}}) {
		t.Error("a clock this process already knows was reported as taken from disk")
	}
	if got := m.lastSync("code", "card"); !got.Equal(newer) {
		t.Errorf("the clock went backwards to %v, want %v", got, newer)
	}
}

// THE CASE THE ONE ABOVE DOES NOT COVER, and the one that actually happened.
//
// TestARenamedDestinationKeepsItsSyncClocks starts from a state file that still
// carries the rename. On a real machine the tally's flush can land in the gap
// between the rename and the config reload, and the flush writes MEMORY — which
// is still keyed by the old name. The file is then back to the old name too, so
// fill has nothing to take and the clocks are gone from both. Every folder reads
// "never synced" to a destination that has been backed up for months, which is
// what the owner reported on 2026-08-11 about a share holding 60GB of his files.
func TestARenamedDestinationKeepsItsClocksWhenAFlushLandsFirst(t *testing.T) {
	synced := time.Date(2026, 8, 11, 17, 34, 0, 0, time.UTC)
	scan := config.ScanMark{TargetUUID: "e92d10cab3f6d6bd"}
	m := newSyncMarks(&config.State{
		MirrorLastSync:  map[string]map[string]time.Time{"code": {"backup-pi": synced}},
		MirrorScanState: map[string]map[string]config.ScanMark{"code": {"backup-pi": scan}},
	})

	before := &config.Config{
		Folders: []config.Folder{{ID: "code"}},
		Targets: []config.Target{{Type: "share", Name: "backup-pi", URL: "//192.168.1.23/backups", Username: "alex"}},
	}
	after := &config.Config{
		Folders: []config.Folder{{ID: "code"}},
		Targets: []config.Target{{Type: "share", Name: "crucial-pi", URL: "//192.168.1.23/backups", Username: "alex"}},
	}

	renamed := renamedTargets(before, after)
	if renamed["backup-pi"] != "crucial-pi" {
		t.Fatalf("the rename was not recognised from the two configs: %v", renamed)
	}
	if !m.renameTarget("backup-pi", "crucial-pi") {
		t.Fatal("no clock was carried across, so the destination reads as never synced")
	}

	// The flush that landed in the gap: the file is back to the old name. fill
	// must not undo the move, and prune then drops the name the config has lost.
	m.fill(&config.State{
		MirrorLastSync:  map[string]map[string]time.Time{"code": {"backup-pi": synced}},
		MirrorScanState: map[string]map[string]config.ScanMark{"code": {"backup-pi": scan}},
	})
	m.prune(after)

	if got := m.lastSync("code", "crucial-pi"); !got.Equal(synced) {
		t.Errorf("the renamed destination's clock is %v, want %v: the dashboard would say nothing is backed up on it", got, synced)
	}
	if got := m.scanMark("code", "crucial-pi", "e92d10cab3f6d6bd"); got.TargetUUID != "e92d10cab3f6d6bd" {
		t.Error("the renamed destination lost what its last pass learned, so the next pass is a full one for nothing")
	}
	if got := m.snapshot(); !got["code"]["backup-pi"].IsZero() {
		t.Error("the old name still has a clock, which state.json would carry for ever")
	}
}

// IT REFUSES TO GUESS. Handing one destination another's clock would report a
// drive as backed up on the strength of a pass that never touched it.
func TestClocksAreNotCarriedAcrossWhenTheRenameIsAmbiguous(t *testing.T) {
	share := func(name, url string) config.Target {
		return config.Target{Type: "share", Name: name, URL: url, Username: "alex"}
	}
	cases := []struct {
		what          string
		before, after []config.Target
	}{
		{
			what:   "two destinations swapping names",
			before: []config.Target{share("card", "//nas/a"), share("pi", "//nas/b")},
			after:  []config.Target{share("pi", "//nas/a"), share("card", "//nas/b")},
		},
		{
			what:   "two destinations at the same place",
			before: []config.Target{share("one", "//nas/a"), share("two", "//nas/a")},
			after:  []config.Target{share("three", "//nas/a"), share("four", "//nas/a")},
		},
		{
			what:   "a destination that moved rather than being renamed",
			before: []config.Target{share("pi", "//nas/a")},
			after:  []config.Target{share("pi-new", "//somewhere-else/a")},
		},
	}
	for _, c := range cases {
		t.Run(c.what, func(t *testing.T) {
			got := renamedTargets(&config.Config{Targets: c.before}, &config.Config{Targets: c.after})
			if len(got) != 0 {
				t.Errorf("guessed a rename from %s: %v", c.what, got)
			}
		})
	}
}
