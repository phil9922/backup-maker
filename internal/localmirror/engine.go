// SPDX-License-Identifier: MIT

// Package localmirror keeps a one-way, versioned mirror of a source folder on
// a mirror-style backup target: a drive attached to this machine or a network
// share (NAS, router USB drive). These are the target kinds syncthing can't
// cover, since it only syncs between paired devices.
package localmirror

import (
	"context"
	"log/slog"
	"os"
	"path"
	"sync"
	"sync/atomic"
	"time"
)

// Engine mirrors one source folder onto one target backend.
type Engine struct {
	FolderID   string
	TargetName string
	TargetType string // "drive" or "share"

	sourcePath string
	backend    Backend
	destRoot   string // slash path inside the backend: <machine>/<label>
	uuid       string
	maxAge     time.Duration
	verify     bool
	pollEvery  time.Duration
	minFree    uint64
	reclaimer  *Reclaimer
	ignore     *Matcher
	counted    func(bytes int64)
	synced     func(at time.Time)
	log        *slog.Logger

	kick chan struct{} // watcher → sync loop nudges

	mu           sync.Mutex
	state        string // scanning | in sync | syncing | offline | wrong-drive
	lastSync     time.Time
	fileErrors   map[string]string
	symlinkCount int
	// selfCopyWarned marks that this destination has already been reported as
	// holding a copy of our own configuration directory, so the warning is
	// logged on the transition rather than once an hour for ever.
	selfCopyWarned bool

	// Live transfer progress, so the dashboard can show a moving bar rather
	// than a binary in-sync/not flag. Totals are known before copying starts
	// (reconcile decides the whole pending set first), so the denominator is
	// real rather than a guess.
	doneFiles  int
	totalFiles int
	doneBytes  int64
	totalBytes int64

	// Bytes written so far for the file currently in flight. Kept separate
	// from doneBytes (and atomic rather than mutex-guarded) because it is
	// written on every 32KB chunk: taking the lock that often, on a path that
	// is already doing network I/O, is exactly the kind of contention worth
	// avoiding. It is folded into doneBytes when the snapshot is taken.
	inFlight atomic.Int64

	// mtime calibration: some servers silently ignore Chtimes; on those,
	// comparing by mtime would recopy everything forever, so we fall back to
	// size + changed-since-last-scan.
	calibrated    bool
	mtimeTrusted  bool
	prevScanStart time.Time
}

type Options struct {
	FolderID    string
	TargetName  string
	TargetType  string // "drive" (default) or "share"
	SourcePath  string
	Backend     Backend
	MachineName string
	Label       string
	UUID        string
	MaxAgeDays  int
	// Verify re-reads every written file and compares checksums. Essential
	// for network shares; unnecessary overhead for local drives.
	Verify bool
	// OfflinePoll is how often an offline target is probed for return
	// (default 5s; use ~30s for network shares to stay gentle on wifi).
	OfflinePoll time.Duration
	// MinFreeBytes keeps this much room free on the destination by deleting
	// the oldest backup history. 0 disables reclaiming entirely.
	MinFreeBytes uint64
	// Reclaimer is shared by every engine writing to the same destination, so
	// concurrent full-disk events don't race each other into deleting.
	Reclaimer *Reclaimer
	// Counted, if set, is called once per file that LANDS INTACT on the
	// destination, with the number of bytes actually written. It feeds the
	// lifetime "how much have you backed up for me" odometer.
	//
	// A callback rather than a direct write to state.json, for the same reason
	// Reclaimer and Log are seams: this package copies files and knows nothing
	// about where the daemon keeps its bookkeeping, and a counter that wrote to
	// disk from inside the copy loop would rewrite state.json once per file.
	// Failed and abandoned copies never reach it — bytes that did not survive
	// were not backed up.
	Counted func(bytes int64)
	// LastSync seeds the clock this engine reports as its last successful sync,
	// from wherever the caller persisted it. Zero means "never synced", which is
	// emphatically not the same as "long ago": status reads an absent timestamp
	// as a destination that has not started yet, and only a real one can make it
	// stale.
	//
	// Seeding matters because the engine holds this in memory alone. Without it
	// every daemon restart tells a drive that has been unplugged for months that
	// it has never been synced at all, which reads as offline for ever.
	LastSync time.Time
	// Synced, if set, is called with the time of each completed pass, whether or
	// not anything needed copying. It is what lets a caller persist LastSync for
	// the next start.
	//
	// A callback rather than a write from in here, for the same reason Counted
	// is one: this package copies files and knows nothing about where the daemon
	// keeps its bookkeeping. A pass that could not run — target away, wrong
	// storage, no room — never reaches it, because nothing was synced.
	Synced  func(at time.Time)
	Ignores []string
	Log     *slog.Logger
}

// engineIgnores are always excluded: syncthing's folder bookkeeping (it never
// syncs these to peers either) and our own artifacts.
var engineIgnores = []string{".stfolder", ".stignore", ".stversions"}

func New(o Options) *Engine {
	o.Ignores = append(append([]string(nil), o.Ignores...), engineIgnores...)
	if o.TargetType == "" {
		o.TargetType = "drive"
	}
	if o.OfflinePoll <= 0 {
		o.OfflinePoll = 5 * time.Second
	}
	return &Engine{
		FolderID:   o.FolderID,
		TargetName: o.TargetName,
		TargetType: o.TargetType,
		sourcePath: o.SourcePath,
		backend:    o.Backend,
		destRoot:   path.Join(sanitize(o.MachineName), sanitize(o.Label)),
		uuid:       o.UUID,
		maxAge:     time.Duration(o.MaxAgeDays) * 24 * time.Hour,
		verify:     o.Verify,
		pollEvery:  o.OfflinePoll,
		minFree:    o.MinFreeBytes,
		reclaimer:  o.Reclaimer,
		ignore:     NewMatcherFor(o.SourcePath, o.Ignores),
		counted:    o.Counted,
		synced:     o.Synced,
		log: o.Log.With("sub", "localmirror", "folder", o.FolderID,
			"target", o.TargetName, "type", o.TargetType),
		kick:         make(chan struct{}, 1),
		state:        "scanning",
		lastSync:     o.LastSync,
		fileErrors:   map[string]string{},
		mtimeTrusted: true,
	}
}

// Run drives the mirror until ctx is cancelled: initial reconcile, then
// event-driven syncs with an hourly full-scan backstop and daily prune.
func (e *Engine) Run(ctx context.Context) {
	defer e.backend.Close()
	go e.watch(ctx)

	rescan := time.NewTicker(time.Hour)
	defer rescan.Stop()
	prune := time.NewTicker(24 * time.Hour)
	defer prune.Stop()
	offlinePoll := time.NewTicker(e.pollEvery)
	defer offlinePoll.Stop()

	e.sync()
	for {
		select {
		case <-ctx.Done():
			return
		case <-e.kick:
			e.sync()
		case <-rescan.C:
			e.sync()
		case <-prune.C:
			if e.online() {
				if err := Prune(e.backend, e.maxAge, time.Now()); err != nil {
					e.log.Warn("version prune failed", "err", err)
				}
			}
		case <-offlinePoll.C:
			// Cheap no-op while online; while offline this is the
			// return detector that triggers catch-up (and, for network
			// shares, the reconnect cadence).
			if !e.online() && checkPresence(e.backend, e.uuid) == presentOK {
				e.log.Info("target returned; catching up")
				e.sync()
			}
		}
	}
}

func (e *Engine) sync() {
	switch checkPresence(e.backend, e.uuid) {
	case wrongDrive:
		e.setState("wrong-drive")
		e.log.Warn("different storage at target location; refusing to write")
		return
	case absent:
		e.setState("offline")
		return
	}

	if !e.calibrated {
		e.calibrateMtime()
		e.calibrated = true
	}

	// Make room before copying rather than discovering the problem mid-file.
	e.ensureHeadroom(0)

	e.setState("syncing")
	copied, removed, err := e.reconcile()
	if err != nil {
		if IsNoSpace(err) {
			// Reclaiming already ran and still couldn't free enough, or there
			// was nothing safe to delete. Say so plainly: a destination that
			// can't be written must never look healthy.
			e.setState("full")
			e.log.Error("destination is full and could not be cleared", "err", err)
			return
		}
		// Mid-sync target loss lands here; the offline poll resumes us.
		e.setState("offline")
		e.log.Warn("sync interrupted", "err", err)
		return
	}
	if copied > 0 || removed > 0 {
		e.log.Info("synced", "copied", copied, "versioned_away", removed)
	}
	now := time.Now()
	e.mu.Lock()
	e.state = "in sync"
	e.lastSync = now
	e.mu.Unlock()
	// Outside the lock: persisting this is the caller's business, and a sync
	// pass must never wait on it.
	e.noteSynced(now)
}

func (e *Engine) online() bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.state != "offline" && e.state != "wrong-drive"
}

func (e *Engine) setState(s string) {
	e.mu.Lock()
	e.state = s
	e.mu.Unlock()
}

func (e *Engine) addFileError(path string, err error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if len(e.fileErrors) < 100 { // bound memory; the count still grows in logs
		e.fileErrors[path] = err.Error()
	}
	e.log.Warn("file error", "path", path, "err", err)
}

func (e *Engine) noteSymlinkSkipped(rel string) {
	e.mu.Lock()
	first := e.symlinkCount == 0
	e.symlinkCount++
	e.mu.Unlock()
	if first {
		e.log.Warn("symlinks are not mirrored to drive/share targets (first: " + rel + ")")
	}
}

// Status is a point-in-time snapshot for the status model.
type Status struct {
	FolderID   string            `json:"folder_id"`
	TargetName string            `json:"target_name"`
	TargetType string            `json:"target_type"`
	State      string            `json:"state"`
	LastSync   time.Time         `json:"last_sync"`
	FileErrors map[string]string `json:"file_errors,omitempty"`
	Symlinks   int               `json:"symlinks_skipped,omitempty"`

	// Progress of the current (or most recent) transfer. Totals are 0 when
	// there was nothing to copy — which is the steady state, not an error.
	DoneFiles  int   `json:"done_files,omitempty"`
	TotalFiles int   `json:"total_files,omitempty"`
	DoneBytes  int64 `json:"done_bytes,omitempty"`
	TotalBytes int64 `json:"total_bytes,omitempty"`
}

// Completion is the percentage of the current transfer that has landed, by
// bytes. It reports 100 when there is nothing pending, so an idle mirror reads
// as complete rather than as zero progress.
func (s Status) Completion() float64 {
	if s.TotalBytes <= 0 {
		if s.TotalFiles > 0 {
			return float64(s.DoneFiles) / float64(s.TotalFiles) * 100
		}
		return 100
	}
	pct := float64(s.DoneBytes) / float64(s.TotalBytes) * 100
	if pct > 100 {
		return 100
	}
	return pct
}

func (e *Engine) Status() Status {
	e.mu.Lock()
	defer e.mu.Unlock()
	errs := make(map[string]string, len(e.fileErrors))
	for k, v := range e.fileErrors {
		errs[k] = v
	}
	// Bytes already written for the file still in flight count as done, so a
	// single huge file advances the bar instead of pinning it until it lands.
	// It can't overshoot in practice (in-flight bytes belong to a file not yet
	// in doneBytes), but a file that grew since the scan would, and a bar that
	// reads past its own total looks broken.
	done := e.doneBytes + e.inFlight.Load()
	if e.totalBytes > 0 && done > e.totalBytes {
		done = e.totalBytes
	}
	return Status{
		FolderID:   e.FolderID,
		TargetName: e.TargetName,
		TargetType: e.TargetType,
		State:      e.state,
		LastSync:   e.lastSync,
		FileErrors: errs,
		Symlinks:   e.symlinkCount,
		DoneFiles:  e.doneFiles,
		TotalFiles: e.totalFiles,
		DoneBytes:  done,
		TotalBytes: e.totalBytes,
	}
}

// beginTransfer publishes the size of the work about to be done.
func (e *Engine) beginTransfer(files int, bytes int64) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.doneFiles, e.totalFiles = 0, files
	e.doneBytes, e.totalBytes = 0, bytes
	e.inFlight.Store(0)
}

// advanceTransfer records one completed file, folding whatever that file
// reported mid-flight into the settled total.
func (e *Engine) advanceTransfer(bytes int64) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.doneFiles++
	e.doneBytes += bytes
	// Zeroed under the same lock Status() holds, so a snapshot can never see
	// this file counted both in doneBytes and in flight.
	e.inFlight.Store(0)
}

// reportInFlight records progress within the file being copied right now. It is
// called once per chunk, so it does no more than a single atomic store.
func (e *Engine) reportInFlight(written int64) { e.inFlight.Store(written) }

// noteCopied reports one file that reached the destination intact, for the
// lifetime odometer. Called only from the success path in reconcile: a copy
// that failed left nothing on the destination, and counting it would inflate
// the one number a user is meant to be able to trust.
func (e *Engine) noteCopied(bytes int64) {
	if e.counted != nil {
		e.counted(bytes)
	}
}

// noteSynced reports a completed pass, so the clock this engine was seeded with
// can be carried across the next restart. Called only where the mirror is
// actually up to date — a pass that found the target away synced nothing, and
// stamping it would claim a backup that never happened.
func (e *Engine) noteSynced(at time.Time) {
	if e.synced != nil {
		e.synced(at)
	}
}

// endTransfer clears progress once a pass finishes. Counters must not linger:
// a stale 40% on an idle mirror would be worse than no bar at all.
func (e *Engine) endTransfer() {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.doneFiles, e.totalFiles = 0, 0
	e.doneBytes, e.totalBytes = 0, 0
	e.inFlight.Store(0)
}

// calibrateMtime probes whether the target honors Chtimes: write a scratch
// file, backdate it, read the mtime back. Servers that ignore SetInfo (some
// router/NAS firmware) get size+recency comparison instead of mtimes.
func (e *Engine) calibrateMtime() {
	const probe = ".backup-maker-probe" + tmpSuffix
	w, err := e.backend.OpenWrite(probe)
	if err != nil {
		return // can't probe now; try again next calibration opportunity
	}
	_, werr := w.Write([]byte("probe"))
	cerr := w.Close()
	if werr != nil || cerr != nil {
		_ = e.backend.Remove(probe)
		return
	}
	want := time.Now().Add(-time.Hour).Truncate(time.Second)
	_ = e.backend.Chtimes(probe, time.Now(), want)
	fi, err := e.backend.Stat(probe)
	_ = e.backend.Remove(probe)
	if err != nil {
		return
	}
	delta := fi.ModTime().Sub(want)
	if delta < 0 {
		delta = -delta
	}
	if delta > mtimeTolerance {
		e.mtimeTrusted = false
		e.log.Warn("target does not preserve file timestamps; using size+recency comparison instead")
	}
}

// shouldCopy decides whether a source file needs (re)copying, honoring the
// calibration result.
func (e *Engine) shouldCopy(srcInfo os.FileInfo, destPath string) bool {
	if e.mtimeTrusted {
		return needsCopy(e.backend, srcInfo, destPath)
	}
	di, err := e.backend.Stat(destPath)
	if err != nil {
		return true
	}
	if di.Size() != srcInfo.Size() {
		return true
	}
	// Same size: recopy only if the source changed since the last completed
	// scan (zero on daemon start → one full recopy, then steady state).
	return srcInfo.ModTime().After(e.prevScanStart)
}

// sanitize keeps names usable on FAT/NTFS/SMB targets.
func sanitize(name string) string {
	out := []rune(name)
	for i, r := range out {
		switch r {
		case '<', '>', ':', '"', '/', '\\', '|', '?', '*':
			out[i] = '_'
		}
	}
	if len(out) == 0 {
		return "unnamed"
	}
	return string(out)
}

// ensureHeadroom deletes the oldest backup history until the destination has
// minFree bytes spare, plus extra for a file about to be written.
//
// Returns whether any space was actually reclaimed, so a caller retrying a
// failed write knows if retrying is worth it.
func (e *Engine) ensureHeadroom(extra uint64) bool {
	if e.reclaimer == nil || (e.minFree == 0 && extra == 0) {
		return false
	}
	reporter, ok := e.backend.(SpaceReporter)
	if !ok {
		return false // backend can't measure space; reclaiming stays off
	}
	avail, _, err := reporter.Usage()
	if err != nil {
		e.log.Warn("could not read free space on destination", "err", err)
		return false
	}
	want := e.minFree + extra
	if avail >= want {
		return false
	}
	freed, deleted := e.reclaimer.Reclaim(e.backend, want-avail, time.Now(), e.log)
	return deleted > 0 && freed > 0
}

// ReclaimNote reports the most recent space reclamation on this destination,
// for the dashboard.
func (e *Engine) ReclaimNote() (time.Time, string) {
	if e.reclaimer == nil {
		return time.Time{}, ""
	}
	return e.reclaimer.LastOutcome()
}
