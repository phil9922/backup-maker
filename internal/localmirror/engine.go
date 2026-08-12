// SPDX-License-Identifier: MIT

// Package localmirror keeps a one-way, versioned mirror of a source folder on
// a mirror-style backup target: a drive attached to this machine or a network
// share (NAS, router USB drive). These are the target kinds syncthing can't
// cover, since it only syncs between paired devices.
package localmirror

import (
	"context"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/phil9922/backup-maker/internal/config"
)

// Engine mirrors one source folder onto one target backend.
type Engine struct {
	FolderID   string
	TargetName string
	TargetType string // "drive" or "share"

	sourcePath string
	backend    Backend
	destRoot   string // slash path inside the backend: <machine>/<label>
	// machineDir is destRoot's first segment on its own — the directory this
	// machine owns on the destination, which is what the claim is about. Kept
	// separately rather than sliced off destRoot at the point of use, because a
	// claim checked against a differently-derived path is a claim on a different
	// directory from the one being written to.
	machineDir  string
	machineName string
	installID   string
	owns        func(string) bool
	uuid        string
	maxAge      time.Duration
	verify      bool
	pollEvery   time.Duration
	minFree     uint64
	reclaimer   *Reclaimer
	ignore      *Matcher
	counted     func(bytes int64)
	synced      func(at time.Time)
	passDone    func(PassMark)
	// destFileHint is how many files the last completed pass found on the
	// destination, used only as the denominator while the current one lists it.
	destFileHint int64
	// lastPrune is when this engine last thinned the version store. In-memory
	// only and zero at start, so every restart prunes once after its first
	// pass. That is deliberate: pruning used to wait on a 24-hour ticker,
	// which needs 24 hours of continuous uptime before its first fire — and a
	// daemon that restarts for every upgrade, reboot and suspend never gets
	// there, so the store grew without bound.
	lastPrune time.Time
	log       *slog.Logger

	// kick carries the source-relative paths the watcher saw in a burst, not just
	// a nudge. The paths are what let a save be propagated without enumerating
	// the whole destination first; an empty slice still means "something changed,
	// go and look properly".
	kick chan []string

	// asked marks a pass somebody requested by hand ("Back up now"), and is
	// carried BESIDE the channel rather than in it.
	//
	// It has to be separate because kick has room for one item: if the watcher
	// has already queued a two-file batch, a manual send is dropped, and the
	// queued batch would take the fast path — so the button would copy the two
	// files the watcher happened to see and call that a backup. Somebody
	// pressing it is asking for a real check, so the flag makes whichever kick
	// arrives next a FULL pass. Read and cleared by the run loop, which is the
	// only caller of sync().
	asked atomic.Bool

	mu           sync.Mutex
	state        string // scanning | in sync | syncing | offline | wrong-drive | name-clash
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

	// Files examined during the current scan. Atomic for the same reason as
	// inFlight: it is bumped once per file on a path already doing network
	// I/O. It exists so a scan that takes minutes can say how far it has got
	// instead of showing a full bar and the word "syncing".
	scanned atomic.Int64

	// scanTotal is the denominator for scanned: how many files this scan has
	// to examine. Known before the first destination round trip, because the
	// source walk finishes and is sorted before anything is compared.
	//
	// A COUNT WITHOUT A DENOMINATOR IS NOT PROGRESS. "scanning, 12,889 files"
	// says nothing about whether that is nearly done or barely started, and on
	// a network share the compare phase is where the minutes go. A destination
	// holding a complete copy spent quarter-hours looking identical to a dead
	// one. 0 means genuinely unknown, which is honest and rare — never a guess.
	scanTotal atomic.Int64

	// phase names what scanned is counting, because the scan has three stages
	// that cost wildly different amounts of time and count different things. A
	// number on screen without its unit is how "checked 0 of 70,827" came to be
	// displayed for minutes on end while the destination was being listed —
	// technically true of the compare loop, which had not started.
	// Guarded by mu with the rest of the display state.
	phase string

	// mtime calibration: some servers silently ignore Chtimes; on those,
	// comparing by mtime would recopy everything forever, so we fall back to
	// size + changed-since-last-scan.
	calibrated    bool
	mtimeTrusted  bool
	prevScanStart time.Time

	// Pass timing, for the log line at the end of a pass. Only ever touched
	// from the sync goroutine, like prevScanStart above, so it takes no lock.
	//
	// SEPARATE FROM phase ON PURPOSE. phase is what the dashboard shows and is
	// worded for someone reading it; a stage here is a stretch of work worth
	// timing, which is not the same carving. The directory sweep is the case
	// that forced the split: it runs with phase still "tidying" because from
	// outside it is the same activity, but it is its own cost and had to be
	// measurable on its own.
	passStart  time.Time
	stageName  string
	stageStart time.Time
	stages     []stageTime
	// dirsListed is how many directories the destination index visited. Logged
	// with the timings because "listing took 26s" is not actionable without it:
	// the useful number is the cost per directory, and that is what says whether
	// the concurrency is working or the tree is simply enormous.
	dirsListed int
}

// stageTime is how long one stretch of a pass took.
type stageTime struct {
	name string
	took time.Duration
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
	Synced func(at time.Time)
	// PrevScanStart seeds the "changed since the last completed scan" clock from
	// wherever the caller persisted it. Zero means never scanned — which on a
	// destination that does not preserve timestamps costs one full recopy of the
	// folder, which is exactly why it is worth carrying across a restart.
	PrevScanStart time.Time
	// MtimeTrusted seeds the timestamp-preservation probe. nil re-probes on the
	// first pass, which is the right answer for a destination never seen before
	// and the wrong one for the same card every morning.
	MtimeTrusted *bool
	// DestFileCount seeds how many files the last completed pass found on the
	// destination, so the listing phase has a denominator to report against
	// instead of counting up from nothing.
	DestFileCount int64
	// PassCompleted, if set, is called with what a FINISHED pass learned, so the
	// caller can persist it. A pass that was interrupted never reaches it, for
	// the same reason Synced is only called by one that synced.
	PassCompleted func(PassMark)
	Ignores       []string
	Log           *slog.Logger
	// InstallID identifies this installation, and Owns reports whether a claim
	// found on a destination belongs to it (counting ids inherited by adopting
	// a machine — see config.State.Owns). Together they are what tells this
	// computer's <machine> directory from another computer's that happens to
	// have the same name.
	//
	// Both empty means no claim checking at all, which is what an older caller
	// and most tests want: the engine then behaves exactly as it did before
	// claims existed.
	InstallID string
	Owns      func(installID string) bool
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
	if o.MaxAgeDays <= 0 {
		// Same convention as syncthing.StaggeredVersioning: unset means the
		// default. A zero must never reach Prune — a maxAge of 0 reads as
		// "every version is too old" and would empty the store.
		o.MaxAgeDays = config.DefaultVersioningMaxDays
	}
	return &Engine{
		FolderID:     o.FolderID,
		TargetName:   o.TargetName,
		TargetType:   o.TargetType,
		sourcePath:   o.SourcePath,
		backend:      o.Backend,
		destRoot:     config.DestRoot(o.MachineName, o.Label),
		machineDir:   config.MachineDir(o.MachineName),
		machineName:  o.MachineName,
		installID:    o.InstallID,
		owns:         o.Owns,
		uuid:         o.UUID,
		maxAge:       time.Duration(o.MaxAgeDays) * 24 * time.Hour,
		verify:       o.Verify,
		pollEvery:    o.OfflinePoll,
		minFree:      o.MinFreeBytes,
		reclaimer:    o.Reclaimer,
		ignore:       NewMatcherFor(o.SourcePath, o.Ignores),
		counted:      o.Counted,
		synced:       o.Synced,
		passDone:     o.PassCompleted,
		destFileHint: o.DestFileCount,
		log: o.Log.With("sub", "localmirror", "folder", o.FolderID,
			"target", o.TargetName, "type", o.TargetType),
		kick:       make(chan []string, 1),
		state:      "scanning",
		lastSync:   o.LastSync,
		fileErrors: map[string]string{},
		// Seeded from the last completed pass where there was one. Without a
		// seed, prevScanStart is zero — which on a destination that does not
		// preserve timestamps means "every same-size file changed" and recopies
		// the whole folder, once per restart, for ever.
		prevScanStart: o.PrevScanStart,
		mtimeTrusted:  o.MtimeTrusted == nil || *o.MtimeTrusted,
		calibrated:    o.MtimeTrusted != nil,
	}
}

// Run drives the mirror until ctx is cancelled: initial reconcile, then
// event-driven syncs with an hourly full-scan backstop. The version store is
// pruned after the first pass and then daily, measured from the last
// successful prune rather than from a ticker.
func (e *Engine) Run(ctx context.Context) {
	defer e.backend.Close()
	go e.watch(ctx)

	rescan := time.NewTicker(time.Hour)
	defer rescan.Stop()
	offlinePoll := time.NewTicker(e.pollEvery)
	defer offlinePoll.Stop()

	e.sync()
	e.pruneIfDue()
	for {
		select {
		case <-ctx.Done():
			return
		case changed := <-e.kick:
			if e.fullPassWanted(changed) {
				e.sync()
				continue
			}
			e.propagate(changed)
		case <-rescan.C:
			e.sync()
			e.pruneIfDue()
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

// fullPassWanted decides what one kick off e.kick means: a full reconcile, or
// the fast path that copies the named files straight over.
//
// A handful of changed files gets copied straight over; anything else — a
// burst, a folder that has never completed a pass, or a pass somebody asked
// for by hand — gets the full pass. The full pass still runs on the hourly
// ticker either way, so only the manual case is load-bearing here.
//
// The manual flag is CLEARED here and nowhere else, so one press buys exactly
// one full pass rather than turning the fast path off for good.
func (e *Engine) fullPassWanted(changed []string) bool {
	if e.asked.Swap(false) {
		return true
	}
	return !e.fastPathUsable(changed)
}

// BackUpNow asks for a full pass as soon as this engine can run one, instead of
// waiting for the hourly rescan. It returns immediately; the pass itself may
// take minutes.
//
// A NUDGE INTO THE RUN LOOP, NEVER A SECOND CALL SITE FOR sync(). Everything
// that copies runs on the one goroutine started by Run, which is what makes
// passes non-overlapping — two reconciles of the same pair at once would race
// each other over the same destination files. So this only ever sets the flag
// and pokes the channel; whether a pass is in flight is decided by that
// goroutine, on its own time.
//
// Dropping the send when the channel is full is correct: a kick is already
// queued, and e.asked has already made it a full pass.
func (e *Engine) BackUpNow() {
	// Before the send, so that whichever kick the loop reads next — the one
	// queued here, or one already waiting — sees it.
	e.asked.Store(true)
	select {
	case e.kick <- nil:
	default:
	}
}

// Busy reports whether a pass is running right now, so a caller can say "it is
// already working" rather than implying a press started something.
func (e *Engine) Busy() bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.state == "scanning" || e.state == "syncing"
}

// pruneIfDue applies the staggered retention if a day has passed since this
// engine last pruned successfully. It is checked after the initial pass and on
// every hourly rescan, NOT from its own 24-hour ticker: a ticker resets with
// the process, so it demanded 24 hours of continuous uptime before the first
// fire — which a machine that upgrades, reboots or suspends daily never
// reaches, and the "~30 days of versions" the docs promise silently became
// "for ever". lastPrune only advances on success, so a failed prune retries
// on the next tick instead of waiting out the day.
func (e *Engine) pruneIfDue() {
	if !e.online() || time.Since(e.lastPrune) < 24*time.Hour {
		return
	}
	if err := Prune(e.backend, e.maxAge, time.Now()); err != nil {
		e.log.Warn("version prune failed", "err", err)
		return
	}
	e.lastPrune = time.Now()
}

// readyToWrite answers the questions that must be settled before anything is
// written to this destination, and is the ONE place they are asked.
//
// Every one of them is a refusal, so a second copy of this sequence is a second
// chance to write somebody else's files. Both the full pass and the fast path
// call it; do not inline it into a third caller.
func (e *Engine) readyToWrite() bool {
	switch checkPresence(e.backend, e.uuid) {
	case wrongDrive:
		e.setState("wrong-drive")
		e.log.Warn("different storage at target location; refusing to write")
		return false
	case absent:
		e.setState("offline")
		return false
	}

	// The second question, and the storage being right does not answer it: this
	// destination may be shared with other computers, and one of them may have
	// the same machine name. Writing into a directory that is theirs means both
	// machines reconcile the same tree against different sources, and each
	// versions the other's files away on every pass.
	if !e.claimMachineDir() {
		return false
	}

	if !e.calibrated {
		e.calibrateMtime()
		e.calibrated = true
	}
	return true
}

func (e *Engine) sync() {
	if !e.readyToWrite() {
		return
	}

	// Make room before copying rather than discovering the problem mid-file.
	e.ensureHeadroom(0)

	// SCANNING, NOT SYNCING, until the work is actually known.
	//
	// Deciding what needs copying costs one Stat per file, and against a
	// network drive that is minutes of round trips before a single byte moves.
	// Reporting "syncing" throughout meant the row showed a state it was not
	// in, with no totals to fill the progress bar — and Completion() returns
	// 100 when it knows of nothing pending, so the bar sat FULL while a fifth
	// of the files were still missing. "syncing / — / never" is unreadable and,
	// worse, reads as finished.
	//
	// The state already existed and the dashboard already draws it as an
	// indeterminate bar; nothing was ever putting the engine into it.
	e.setState("scanning")
	e.scanned.Store(0)
	e.scanTotal.Store(0) // unknown until the source walk finishes
	e.beginPass()
	copied, removed, err := e.reconcile()
	// However this pass ended, it is no longer scanning. Deferred rather than
	// written at each exit so a return added later cannot forget it.
	defer e.endScan()
	if err != nil {
		if IsNoSpace(err) {
			// Reclaiming already ran and still couldn't free enough, or there
			// was nothing safe to delete. Say so plainly: a destination that
			// can't be written must never look healthy.
			e.setState("full")
			e.log.Error("destination is full and could not be cleared",
				"took", time.Since(e.passStart).Round(time.Millisecond), "err", err)
			return
		}
		// Mid-sync target loss lands here; the offline poll resumes us.
		//
		// With how long it lasted: a pass that dies after ten minutes and one that
		// dies immediately want different investigations, and the message alone
		// cannot tell them apart.
		e.setState("offline")
		e.log.Warn("sync interrupted",
			"took", time.Since(e.passStart).Round(time.Millisecond), "err", err)
		return
	}
	e.logPass(copied, removed)
	now := time.Now()
	e.mu.Lock()
	e.state = "in sync"
	e.lastSync = now
	e.mu.Unlock()
	// Outside the lock: persisting this is the caller's business, and a sync
	// pass must never wait on it.
	e.noteSynced(now)
}

// claimMachineDir makes sure this machine's directory on the destination is
// ours before anything is written into it, and reports whether the sync may go
// ahead.
//
// AN UNCLAIMED DIRECTORY IS TAKEN, NOT REFUSED. Every destination that was in
// service before claims existed has no claim file, and refusing those would
// mean that replacing the binary stopped every working backup on the machine.
// Setup asks the user about the same situation, because there a human is
// present to answer; here there is nobody, and the safe default is the other
// one. The gate that still holds is the marker checked just above: this only
// ever claims a directory on storage already recognised as this target's.
func (e *Engine) claimMachineDir() bool {
	if e.owns == nil || e.installID == "" {
		// No identity to claim with — an engine built by an older caller, or by
		// a test that does not exercise this. Behave exactly as before.
		return true
	}
	st, err := ClaimIfUnclaimed(e.backend, e.machineDir, e.installID, e.machineName, e.owns)
	if err != nil {
		e.setState("offline")
		e.log.Warn("could not claim this computer's folder on the destination", "err", err)
		return false
	}
	if st != ClaimOurs {
		e.setState("name-clash")
		e.log.Error("another computer already backs up to this destination under the same name; refusing to write, because both computers would delete each other's files",
			"folder", e.machineDir)
		return false
	}
	return true
}

func (e *Engine) online() bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.state != "offline" && e.state != "wrong-drive" && e.state != "name-clash"
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

	// ScannedFiles is how many files the current scan has examined, and
	// ScanTotalFiles how many it has to examine. Only meaningful while State is
	// "scanning", when no transfer totals exist yet. ScanTotalFiles is 0 while
	// the source is still being walked and the denominator is not yet known.
	ScannedFiles   int64 `json:"scanned_files,omitempty"`
	ScanTotalFiles int64 `json:"scan_total_files,omitempty"`
	// Phase names the stage the scan is in: "source" while the folder is being
	// walked, "listing" while the destination is, "comparing" while the two are
	// matched up. Empty once copying starts.
	Phase string `json:"phase,omitempty"`

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

		ScannedFiles:   e.scanned.Load(),
		ScanTotalFiles: e.scanTotal.Load(),
		Phase:          e.phase,
	}
}

// beginTransfer publishes the size of the work about to be done. Reaching here
// is what turns "scanning" into "syncing": the totals now exist, so the bar can
// stop guessing and show real progress.
func (e *Engine) beginTransfer(files int, bytes int64) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.doneFiles, e.totalFiles = 0, files
	e.doneBytes, e.totalBytes = 0, bytes
	e.inFlight.Store(0)
	e.phase = "" // the scan is over; bytes are the unit from here
	if e.state == "scanning" {
		e.state = "syncing"
	}
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

// PassMark is what a completed pass learned and is entitled to carry forward.
type PassMark struct {
	// PassStart is when the pass BEGAN. Not when it ended: the end time would
	// be blind to every file edited while the pass was running, and the next
	// pass would skip them.
	PassStart time.Time
	// MtimeTrusted is the calibration result, so the next start does not
	// re-probe — and, on a destination that failed the probe, does not spend a
	// restart recopying every same-size file before rediscovering it.
	MtimeTrusted bool
	// DestFiles is how many files this pass found on the destination.
	DestFiles int64
}

// notePassCompleted hands the caller what this pass learned, for persisting.
//
// Reached only at the very end of a successful reconcile, past every failure
// return — for the same reason noteSynced is. A pass that was interrupted
// examined an unknown fraction of the tree and has no business advancing a
// clock that means "everything up to here has been checked".
func (e *Engine) notePassCompleted(mk PassMark) {
	if e.passDone != nil {
		e.passDone(mk)
	}
}

// beginScanPhase publishes which stage of the scan is running and how much of
// it there is. total 0 means the denominator is genuinely unknown — a first
// pass has no prior count to compare against, and an invented one draws a bar
// that jumps backwards when the real number arrives.
func (e *Engine) beginScanPhase(name string, total int64) {
	e.beginStage(name)
	e.scanned.Store(0)
	e.scanTotal.Store(total)
	e.mu.Lock()
	e.phase = name
	e.mu.Unlock()
}

// beginPass starts the clock for a pass and drops the previous pass's stages.
func (e *Engine) beginPass() {
	now := time.Now()
	e.passStart = now
	e.stageName, e.stageStart = "", now
	e.stages = e.stages[:0]
}

// beginStage closes the stage that is ending, recording what it cost, and opens
// the next. Called for every scan phase, and separately for stretches that share
// a phase with the one before them.
func (e *Engine) beginStage(name string) {
	now := time.Now()
	if e.stageName != "" {
		e.stages = append(e.stages, stageTime{e.stageName, now.Sub(e.stageStart)})
	}
	e.stageName, e.stageStart = name, now
}

// slowPass is when a pass is worth reporting on its own account, having neither
// copied nor removed anything. Well above a healthy pass over a large folder on
// a network share, so a quiet machine stays quiet in the log.
const slowPass = 30 * time.Second

// logPass says how long a pass took and where the time went.
//
// AT INFO EVEN WHEN NOTHING WAS COPIED, IF IT WAS SLOW. A pass that copied
// nothing used to log nothing whatsoever, which is how a destination could spend
// eleven and a half minutes deciding it had nothing to do and leave no trace of
// it: from the log the time was simply missing, and from the dashboard it was a
// phase name with a counter that had stopped moving. Those minutes are the
// symptom people report as "it has stopped working", so they have to be
// recoverable afterwards rather than only observable live.
//
// Quiet otherwise. This runs on every pass of every destination, and a line per
// pass saying nothing happened quickly is how a log stops being read.
func (e *Engine) logPass(copied, removed int) {
	e.beginStage("") // close the last stage
	took := time.Since(e.passStart)
	args := []any{"took", took.Round(time.Millisecond), "copied", copied, "versioned_away", removed}
	if e.dirsListed > 0 {
		args = append(args, "dirs_listed", e.dirsListed)
	}
	for _, s := range e.stages {
		args = append(args, s.name, s.took.Round(time.Millisecond))
	}
	if copied > 0 || removed > 0 || took >= slowPass {
		e.log.Info("synced", args...)
		return
	}
	e.log.Debug("synced", args...)
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

// endScan puts the scan counters away when a pass stops scanning, for the same
// reason endTransfer exists: a number left on screen describes work that is no
// longer happening.
//
// THE BUG THIS FIXES, and it is the one people report. phase was only ever
// cleared by beginTransfer — that is, only when the pass found something to
// copy. A pass that found nothing left phase on "tidying" for ever, and the
// dashboard narrates the phase whenever it is set: a folder that was fully
// backed up and completely idle sat there saying "checking for deleted files:
// 72,555" beside an animated bar, indefinitely, until something changed.
//
// So the machine looked busy and stuck at the same time, which is exactly how it
// gets described — "it hasn't backed up in ten minutes and it's just sitting on
// checking for deleted files". Nothing was wrong: it had nothing to do, and the
// screen was still describing the last thing it did.
//
// Called on every exit from a pass, including the failures. An offline
// destination that stopped mid-listing must not keep narrating the listing.
func (e *Engine) endScan() {
	e.scanned.Store(0)
	e.scanTotal.Store(0)
	e.mu.Lock()
	defer e.mu.Unlock()
	e.phase = ""
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

// sanitize keeps names usable on FAT/NTFS/SMB targets.

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
