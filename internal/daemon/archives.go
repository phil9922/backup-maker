// SPDX-License-Identifier: MIT

package daemon

import (
	"context"
	"time"

	"github.com/phil9922/backup-maker/internal/archive"
	"github.com/phil9922/backup-maker/internal/config"
	"github.com/phil9922/backup-maker/internal/localmirror"
)

// archiveLoop runs scheduled full-snapshot archives. Checks for due jobs
// every 30s; overdue jobs (machine was asleep/off) run at the next check.
func (d *daemon) archiveLoop(ctx context.Context) {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			d.runDueArchives()
		}
	}
}

func (d *daemon) runDueArchives() {
	cfg := d.currentCfg()
	for _, job := range cfg.Archives {
		every, err := config.ParseEvery(job.Every)
		if err != nil {
			continue // validation rejects this; belt and braces
		}
		d.mu.Lock()
		last := d.state.ArchiveLastRun[job.Name]
		d.mu.Unlock()
		if !last.IsZero() && time.Since(last) < every {
			continue
		}
		d.runArchive(cfg, job)
	}
}

// runArchive executes one job against its target and records the result.
func (d *daemon) runArchive(cfg *config.Config, job config.Archive) {
	var target *config.Target
	for i := range cfg.Targets {
		if cfg.Targets[i].Name == job.Target {
			target = &cfg.Targets[i]
			break
		}
	}
	res := archive.Result{ArchiveName: job.Name, When: time.Now()}
	if target == nil {
		res.Err = "target not found: " + job.Target
		d.recordArchiveResult(res, false)
		return
	}

	backend, _, _, err := d.buildBackend(*target)
	if err != nil {
		res.Err = err.Error()
		d.recordArchiveResult(res, false)
		return
	}
	defer backend.Close()

	// Same foreign-storage protection as the mirror engine: verify the
	// target's marker before writing anything.
	d.mu.Lock()
	wantUUID := d.state.DriveTargetUUIDs[target.Name]
	password := d.state.ArchivePasswords[job.Name]
	d.mu.Unlock()
	m, err := localmirror.ReadMarker(backend)
	if err != nil {
		res.Err = "target offline"
		d.recordArchiveResult(res, false)
		return
	}
	if m.TargetUUID != wantUUID {
		res.Err = "different storage at target location; refusing to write"
		d.recordArchiveResult(res, false)
		return
	}

	res = archive.Run(backend, cfg, job, password, d.log)
	d.recordArchiveResult(res, res.Err == "")
}

// recordArchiveResult stores the outcome for status displays, and on success
// persists the run time so the schedule survives restarts.
func (d *daemon) recordArchiveResult(res archive.Result, ok bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.archiveResults == nil {
		d.archiveResults = map[string]archive.Result{}
	}
	d.archiveResults[res.ArchiveName] = res
	if ok {
		if d.state.ArchiveLastRun == nil {
			d.state.ArchiveLastRun = map[string]time.Time{}
		}
		d.state.ArchiveLastRun[res.ArchiveName] = res.When
		_ = d.state.Save()
	}
}

// archiveStatus snapshots job state for the status collector.
func (d *daemon) archiveStatus() ([]archive.Result, map[string]time.Time) {
	d.mu.Lock()
	defer d.mu.Unlock()
	out := make([]archive.Result, 0, len(d.archiveResults))
	for _, r := range d.archiveResults {
		out = append(out, r)
	}
	lastRuns := make(map[string]time.Time, len(d.state.ArchiveLastRun))
	for k, v := range d.state.ArchiveLastRun {
		lastRuns[k] = v
	}
	return out, lastRuns
}
