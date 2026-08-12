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
		// A paused schedule does not run, and that is the whole of it: the
		// password, the retention count and the folder it covers are all still
		// there, waiting to be resumed.
		if job.Paused {
			continue
		}
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
		// Skipped rather than waited for: a manual "Back up now" of this same
		// schedule is already writing it, and the next tick is 30 seconds away.
		if !d.claimArchiveRun(job.Name) {
			continue
		}
		func() {
			defer d.releaseArchiveRun(job.Name)
			d.runArchive(cfg, job)
		}()
	}
}

// claimArchiveRun records that a schedule is executing and reports whether the
// claim was granted.
//
// A LOCK, NOT JUST A DASHBOARD FLAG. It began as the latter — a way for the
// panel to say "running", because LastRun is only written when a run COMPLETES
// and a job part-way through a multi-gigabyte zip otherwise reported "never
// run". Nothing needed it to exclude anything, because the scheduler ran jobs
// one at a time on its own goroutine.
//
// "Back up now" ends that: it starts a run from a dashboard request while the
// scheduler may be starting the same one. Two runs of one job write the same
// temp file (archive.Run derives it from the job name and the second), so each
// would corrupt the other's zip and one would delete the file the other was
// still writing. Every run — scheduled or by hand — goes through this, and the
// loser does not run.
func (d *daemon) claimArchiveRun(name string) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.archiveRunning == nil {
		d.archiveRunning = map[string]time.Time{}
	}
	if _, running := d.archiveRunning[name]; running {
		return false
	}
	d.archiveRunning[name] = time.Now()
	return true
}

// releaseArchiveRun ends a claim. Every caller defers it: a claim left behind
// by a panicking run would stop that schedule for the life of the daemon.
func (d *daemon) releaseArchiveRun(name string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	delete(d.archiveRunning, name)
}

// archiveRunningSnapshot is the collector the status model reads.
func (d *daemon) archiveRunningSnapshot() map[string]time.Time {
	d.mu.Lock()
	defer d.mu.Unlock()
	out := make(map[string]time.Time, len(d.archiveRunning))
	for k, v := range d.archiveRunning {
		out[k] = v
	}
	return out
}

// noteArchiveProgress records how far a running snapshot has got.
//
// Kept in memory only, and dropped the moment the run ends: it describes work
// in flight, and a figure left behind by a finished run would be a bar frozen
// at whatever it happened to reach. Nothing here writes to disk — the callback
// fires once per file, and a snapshot is tens of thousands of them.
func (d *daemon) noteArchiveProgress(name string, p archive.Progress) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.archiveProgress == nil {
		d.archiveProgress = map[string]archive.Progress{}
	}
	d.archiveProgress[name] = p
}

func (d *daemon) clearArchiveProgress(name string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	delete(d.archiveProgress, name)
}

// archiveProgressSnapshot is the collector the status model reads.
func (d *daemon) archiveProgressSnapshot() map[string]archive.Progress {
	d.mu.Lock()
	defer d.mu.Unlock()
	out := make(map[string]archive.Progress, len(d.archiveProgress))
	for k, v := range d.archiveProgress {
		out[k] = v
	}
	return out
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

	backend, _, _, err := d.buildBackend(*target, d.shareCredentials())
	if err != nil {
		res.Err = err.Error()
		d.recordArchiveResult(res, false)
		return
	}
	defer backend.Close()

	// Same foreign-storage protection as the mirror engine — and asked through
	// the same function, deliberately. This used to be an inline marker
	// comparison, which refused correctly but left the rule written down in two
	// places; the one that matters most is the one that would silently drift.
	// Recognize is built on the very checkPresence the mirror's copy path uses,
	// so a snapshot cannot end up with a different idea of "is this our drive"
	// than the mirror beside it.
	//
	// It also tells the truth about a case the old check got wrong: a
	// REFORMATTED destination is mounted and readable with no marker on it,
	// which reported as "target offline" — sending the user to look for an
	// unplugged cable when the real answer is that the storage is not the
	// storage they think it is.
	d.mu.Lock()
	wantUUID := d.state.DriveTargetUUIDs[target.Name]
	password := d.state.ArchivePasswords[job.Name]
	d.mu.Unlock()
	switch localmirror.Recognize(backend, wantUUID) {
	case localmirror.Ours:
	case localmirror.Offline:
		res.Err = "target offline"
		d.recordArchiveResult(res, false)
		return
	default: // Foreign
		res.Err = "different storage at target location; refusing to write"
		d.recordArchiveResult(res, false)
		return
	}

	res = archive.Run(backend, cfg, job, password, d.log, func(p archive.Progress) {
		d.noteArchiveProgress(job.Name, p)
	})
	d.clearArchiveProgress(job.Name)
	d.recordArchiveResult(res, res.Err == "")
}

// recordArchiveResult stores the outcome for status displays, and on success
// persists the run time so the schedule survives restarts.
//
// A finished snapshot also feeds the lifetime odometer: one file (the zip that
// now sits on the destination) of StoredBytes. Counting its thousands of source
// entries and their uncompressed size instead would claim more data was written
// than the destination ever received, and a failed run — which leaves nothing
// behind — contributes nothing at all.
func (d *daemon) recordArchiveResult(res archive.Result, ok bool) {
	if ok && d.tally != nil {
		d.tally.add(1, res.StoredBytes)
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.archiveResults == nil {
		d.archiveResults = map[string]archive.Result{}
	}
	d.archiveResults[res.ArchiveName] = res
	if ok {
		_ = d.updateState(func(s *config.State) error {
			if s.ArchiveLastRun == nil {
				s.ArchiveLastRun = map[string]time.Time{}
			}
			s.ArchiveLastRun[res.ArchiveName] = res.When
			return nil
		})
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
