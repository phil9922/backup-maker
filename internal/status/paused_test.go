// SPDX-License-Identifier: MIT

package status

import (
	"log/slog"
	"testing"
	"time"

	"github.com/phil9922/backup-maker/internal/localmirror"
	"github.com/phil9922/backup-maker/internal/syncthing"

	"github.com/phil9922/backup-maker/internal/config"
)

// pausedConfig is one folder mirrored to two destinations, with the copy to
// "sdcard" paused and the copy to "nas" running.
func pausedConfig() *config.Config {
	return &config.Config{
		General:  config.General{MachineName: "my-laptop"},
		Defaults: config.Defaults{StaleAfterDays: 7},
		Folders: []config.Folder{{
			ID: "kqz3d-8xh2p", Label: "photos", Path: "/home/alex/photos",
			PausedTargets: []string{"sdcard"},
		}},
		Targets: []config.Target{
			{Type: "drive", Name: "sdcard", Path: "/media/alex/SDCARD"},
			{Type: "share", Name: "nas", URL: "//nas/backups"},
		},
	}
}

func pausedCollector(cfg *config.Config, lastSync time.Time) *Collector {
	return &Collector{
		Cfg:      func() *config.Config { return cfg },
		Client:   func() *syncthing.Client { return nil },
		Engines:  func() []*localmirror.Engine { return nil },
		LastSync: func(string, string) time.Time { return lastSync },
	}
}

func rowFor(t *testing.T, m Model, target string) Row {
	t.Helper()
	for _, r := range m.Rows {
		if r.TargetName == target {
			return r
		}
	}
	t.Fatalf("no row for %q in %+v", target, m.Rows)
	return Row{}
}

// THE CENTRAL RULE OF THIS TABLE: the state column answers "are my files safe",
// and a destination that is no longer being sent new work is not one where they
// are. A paused pair must therefore never read as "backed up" — and must not be
// drawn as a fault either, because somebody chose it, exactly as they choose to
// pause a snapshot schedule.
func TestAPausedMirrorIsNotBackedUpAndIsNotAFault(t *testing.T) {
	m := pausedCollector(pausedConfig(), time.Time{}).Collect()

	row := rowFor(t, m, "sdcard")
	if !row.Paused {
		t.Error("the row does not carry paused:true, so the control has nothing to draw itself from")
	}
	if row.State != "paused" {
		t.Errorf("row state = %q, want %q", row.State, "paused")
	}
	if got := RowLabel(row); got == "backed up" {
		t.Error("a paused mirror reads as \"backed up\": nothing saved since it was paused " +
			"is reaching that destination, and this column is the one somebody trusts")
	} else if got != "paused" {
		t.Errorf("RowLabel = %q, want %q", got, "paused")
	}
	if got := RowHealth(row); got != "muted" {
		t.Errorf("RowHealth = %q, want \"muted\" — a deliberate stop is neither working nor broken, "+
			"and everything that falls off the end of that switch is drawn red", got)
	}
}

// Every row in the model carries the flag, whether or not it is paused. A row
// that simply left the key out would be indistinguishable from a daemon that has
// never heard of pausing, and the page would have to guess.
func TestEveryMirrorRowSaysWhetherItIsPaused(t *testing.T) {
	m := pausedCollector(pausedConfig(), time.Time{}).Collect()
	if len(m.Rows) != 1 {
		// Only the paused pair produces a row here: the running one has no
		// engine in this test, and rows for running pairs come from engines.
		t.Fatalf("got %d rows, want 1: %+v", len(m.Rows), m.Rows)
	}
	if !rowFor(t, m, "sdcard").Paused {
		t.Error("the paused pair does not say so")
	}

	// And the same collector with nothing paused publishes an explicit false.
	cfg := pausedConfig()
	cfg.Folders[0].PausedTargets = nil
	for _, r := range pausedCollector(cfg, time.Time{}).Collect().Rows {
		if r.Paused {
			t.Errorf("%s → %s reads as paused with nothing paused", r.FolderLabel, r.TargetName)
		}
	}
}

// A paused pair is not overdue, it is off. Staleness is what the desktop alert
// and the "needs attention" headline key on, so calling a deliberate stop stale
// would interrupt somebody about a decision they made — and train them to ignore
// the alert that matters.
func TestAPausedMirrorIsNeverStale(t *testing.T) {
	// Last backed up long past the staleness threshold, which for any running
	// destination would be "stale" on the first collection.
	lastSync := time.Now().Add(-90 * 24 * time.Hour)
	m := pausedCollector(pausedConfig(), lastSync).Collect()

	row := rowFor(t, m, "sdcard")
	if row.Stale || row.State == "stale" {
		t.Errorf("a mirror paused three months ago reads as stale: %+v", row)
	}
	// And what the alerter actually reads — the destination card — must not say
	// so either. brokenState() fires on "stale" and "full".
	for _, ti := range m.Targets {
		if ti.Name == "sdcard" && (ti.State == "stale" || ti.State == "full") {
			t.Errorf("the destination card reads %q, which raises a backups-stopped alert", ti.State)
		}
	}
	// But it still says WHEN it was last backed up. "never" on a destination
	// holding a full copy is the sentence this program must never produce.
	if !row.LastSeen.Equal(lastSync) {
		t.Errorf("last seen = %v, want the recorded %v", row.LastSeen, lastSync)
	}
}

// A destination is only "paused" as a whole when every folder pointed at it is.
// One paused folder must not make a destination that is still mirroring another
// folder look switched off — and must not make it look worse than it is, because
// the destination cards are what the dashboard's headline is built from.
func TestADestinationIsOnlyPausedWhenEveryFolderOnItIs(t *testing.T) {
	cfg := pausedConfig()
	cfg.Folders = append(cfg.Folders, config.Folder{
		ID: "b7m2p-x91qd", Label: "code", Path: "/home/alex/code",
	})
	// "code" is mirrored to sdcard and is not paused; it has no engine here, so
	// it contributes no row — which is the same shape as a destination that has
	// not been opened yet, and is exactly when a wrong roll-up would show.
	m := pausedCollector(cfg, time.Time{}).Collect()

	var sdcard TargetInfo
	for _, ti := range m.Targets {
		if ti.Name == "sdcard" {
			sdcard = ti
		}
	}
	if sdcard.State != "paused" {
		// With only the paused row present, "paused" is the honest roll-up.
		t.Errorf("sdcard rolled up to %q, want \"paused\"", sdcard.State)
	}
	if RowHealth(Row{State: sdcard.State}) == "bad" {
		t.Error("a paused destination is drawn as a fault, which puts \"needs attention\" " +
			"at the top of a page where nothing is wrong")
	}
	// And a destination with a healthy row beside a paused one takes the healthy
	// one's state: paused ranks below everything, so it can never be the thing a
	// destination is described by while it is still copying something.
	rolled, _ := rollUp([]Row{
		{TargetName: "nas", State: "paused", Paused: true},
		{TargetName: "nas", State: "in sync"},
	}, "nas")
	if rolled != "in sync" {
		t.Errorf("a destination still mirroring one folder rolled up to %q", rolled)
	}
	rolled, _ = rollUp([]Row{
		{TargetName: "nas", State: "in sync"},
		{TargetName: "nas", State: "paused", Paused: true},
	}, "nas")
	if rolled != "in sync" {
		t.Errorf("the roll-up depends on row order: got %q", rolled)
	}
}

// A destination this machine is refusing to write to is a fault about the
// storage. A pair that is paused is not being written to anyway, so the refusal
// must not repaint its row red — nor may a paused row claim a wake-up or a
// first backup is in progress.
func TestARefusedDestinationDoesNotRepaintAPausedRow(t *testing.T) {
	cfg := pausedConfig()
	cfg.Targets[0].MAC = "aa:bb:cc:dd:ee:ff"
	col := pausedCollector(cfg, time.Now().Add(-time.Hour))
	col.Refused = func() map[string]string { return map[string]string{"sdcard": "wrong-drive"} }

	row := rowFor(t, col.Collect(), "sdcard")
	if row.State != "paused" {
		t.Errorf("row state = %q; a refusal repainted a mirror that is switched off", row.State)
	}
	if row.WakeEnabled || row.FirstBackup {
		t.Errorf("a paused row claims activity: %+v", row)
	}
}

// The engine and the config can disagree for the couple of seconds between the
// pause being saved and the next config apply tearing the engine down. The
// config is the honest answer in that window: the row must not go on reporting
// "backed up" for a mirror the user has just switched off.
func TestARunningEngineIsOverriddenByThePauseUntilItIsTornDown(t *testing.T) {
	cfg := pausedConfig()
	e := localmirror.New(localmirror.Options{
		FolderID: "kqz3d-8xh2p", TargetName: "sdcard", TargetType: "drive",
		SourcePath: t.TempDir(), Backend: localmirror.NewLocalFS(t.TempDir()),
		MachineName: "my-laptop", Label: "photos", UUID: "uuid-1",
		Log: slog.New(slog.DiscardHandler),
	})
	col := pausedCollector(cfg, time.Time{})
	col.Engines = func() []*localmirror.Engine { return []*localmirror.Engine{e} }

	m := col.Collect()
	if n := len(m.Rows); n != 1 {
		t.Fatalf("got %d rows, want 1 — the pair was rowed twice: %+v", n, m.Rows)
	}
	row := rowFor(t, m, "sdcard")
	if !row.Paused || row.State != "paused" {
		t.Errorf("row = %+v, want the config's answer while the engine is still up", row)
	}
}
