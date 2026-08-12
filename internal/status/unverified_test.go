// SPDX-License-Identifier: MIT

package status

import (
	"testing"
	"time"

	"github.com/phil9922/backup-maker/internal/archive"
	"github.com/phil9922/backup-maker/internal/config"
	"github.com/phil9922/backup-maker/internal/localmirror"
	"github.com/phil9922/backup-maker/internal/syncthing"
)

// A snapshot that was written but never read back is not a failure — the zip is
// on the destination and very probably fine — so its state is honestly "ok".
// That is exactly why it needs a flag of its own: without one, the only thing
// this page would say about a run that skipped the check it promises is "backed
// up", which is the reassurance that particular run has not earned.
func TestAnUnverifiedSnapshotSaysSoOnTheDashboardRow(t *testing.T) {
	cfg := &config.Config{
		General:  config.General{MachineName: "my-laptop"},
		Archives: []config.Archive{{Name: "weekly-code", Target: "nas", Every: "weekly"}},
		Folders:  []config.Folder{{ID: "f1", Path: "/home/alex/code", Label: "code"}},
	}
	ran := time.Now().Add(-time.Hour)
	why := "the snapshot was written but not checked: reading it back needs about 57.0GB in /tmp"

	row := func(res archive.Result) ArchiveRow {
		t.Helper()
		col := &Collector{
			Cfg:     func() *config.Config { return cfg },
			Client:  func() *syncthing.Client { return nil },
			Engines: func() []*localmirror.Engine { return nil },
			Archives: func() ([]archive.Result, map[string]time.Time) {
				return []archive.Result{res}, map[string]time.Time{"weekly-code": ran}
			},
		}
		m := col.Collect()
		if len(m.Archives) != 1 {
			t.Fatalf("collected %d schedules, want 1", len(m.Archives))
		}
		return m.Archives[0]
	}

	got := row(archive.Result{ArchiveName: "weekly-code", File: "x.zip", Unverified: why})
	if got.State != "ok" {
		t.Errorf("state = %q; a written snapshot is not a failed one", got.State)
	}
	if !got.Unverified {
		t.Fatal("the row does not say the snapshot was never checked")
	}
	if got.UnverifiedReason != why {
		t.Errorf("the reason was lost: %q", got.UnverifiedReason)
	}
	if got.Detail != why {
		t.Errorf("detail = %q; every surface that renders a schedule's detail should say this without being taught to", got.Detail)
	}

	// A run that WAS checked says nothing, or the flag means nothing.
	if checked := row(archive.Result{ArchiveName: "weekly-code", File: "x.zip"}); checked.Unverified {
		t.Error("a checked snapshot is flagged as unchecked")
	}
	// Nor does a FAILED run: it wrote nothing, so there is nothing unchecked,
	// and it already has a state and a detail of its own.
	failed := row(archive.Result{ArchiveName: "weekly-code", Err: "target offline"})
	if failed.Unverified || failed.State != "failed" {
		t.Errorf("a failed run reported as unchecked: state %q, unverified %v", failed.State, failed.Unverified)
	}
	if failed.Detail != "target offline" {
		t.Errorf("the failure's own detail was overwritten: %q", failed.Detail)
	}
}
