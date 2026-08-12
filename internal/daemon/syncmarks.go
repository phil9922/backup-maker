// SPDX-License-Identifier: MIT

package daemon

import (
	"sync"
	"time"

	"github.com/phil9922/backup-maker/internal/config"
	"github.com/phil9922/backup-maker/internal/localmirror"
)

// syncMarks remembers when each folder last completed a sync to each drive/share
// destination, across restarts.
//
// WHY IT EXISTS. The mirror engine keeps that time in memory alone, so every
// restart — a reboot, an upgrade, a crash, the Restart=always policy doing its
// job — used to reset it to "never". A destination with no last-sync time cannot
// be judged stale (status.Collect will not call an absent timestamp ancient, and
// must not), so a card left out of a laptop for months reported "offline" for
// ever and the one alert that matters could never fire. Paired machines were
// never affected: their last-seen comes from the sync engine's own on-disk
// statistics.
//
// Memory is authoritative while the daemon runs, and state.json is written on
// the tally's flush cadence rather than on every sync (see tally.go): a busy
// folder syncs every few seconds, and losing a few seconds of timestamp to a
// hard kill costs nothing while rewriting state.json that often costs a disk.
type syncMarks struct {
	mu sync.Mutex
	// at is folder id -> target name -> last successful sync. Nested rather
	// than one joined key on purpose: target names are free-form text from
	// config.toml, folder ids can be hand-edited to anything, and so no
	// delimiter is safe against a name containing it. Nesting makes a collision
	// — one destination inheriting another's clock — impossible rather than
	// unlikely, and prunes a folder at a time.
	at map[string]map[string]time.Time
	// scans is folder id -> target name -> what the last COMPLETED pass learned.
	// Alongside `at` rather than merged into it: `at` is the user-facing clock
	// and this is engine bookkeeping, and the two have different rules about
	// when they may be written.
	scans map[string]map[string]config.ScanMark
}

// newSyncMarks seeds from persisted state, which is the entire point: the
// engines started moments later get their clocks back.
func newSyncMarks(s *config.State) *syncMarks {
	m := &syncMarks{
		at:    map[string]map[string]time.Time{},
		scans: map[string]map[string]config.ScanMark{},
	}
	for folder, byTarget := range s.MirrorScanState {
		for target, mk := range byTarget {
			if m.scans[folder] == nil {
				m.scans[folder] = map[string]config.ScanMark{}
			}
			m.scans[folder][target] = mk
		}
	}
	for folder, byTarget := range s.MirrorLastSync {
		for target, when := range byTarget {
			if when.IsZero() {
				continue // a zero time means never, and never is not a mark
			}
			if m.at[folder] == nil {
				m.at[folder] = map[string]time.Time{}
			}
			m.at[folder][target] = when
		}
	}
	return m
}

// renamedTargets works out which destinations were renamed between two configs,
// as old name -> new name.
//
// FROM WHERE THE DESTINATION IS, because that is the one thing a rename does not
// change. Matching on the recorded UUID cannot work here: the rename has already
// moved that entry to the new name, so the old name looks like a destination
// nobody has ever seen. The location is what stays put.
//
// IT REFUSES TO GUESS. A location that appears twice in either config, a name
// that is still live in the other one — anything that is not plainly one
// destination that changed its name — is left out, and the pair simply keeps the
// behaviour it had before: one unnecessary full pass, which copies nothing
// because the files are already there. Getting it wrong would hand one
// destination another's clock and report a drive as backed up on the strength of
// a pass that never touched it, which is the one answer this program may never
// give.
func renamedTargets(before, after *config.Config) map[string]string {
	if before == nil || after == nil {
		return nil
	}
	oldByPlace, oldNames := placesAndNames(before)
	newByPlace, newNames := placesAndNames(after)
	renamed := map[string]string{}
	for place, from := range oldByPlace {
		to, still := newByPlace[place]
		switch {
		case !still, from == to, from == "", to == "":
			continue
		case newNames[from], oldNames[to]:
			// Both names are live somewhere in the other config, so this is two
			// destinations swapping or shuffling names rather than one being
			// renamed. Not a case we can move clocks for safely.
			continue
		}
		renamed[from] = to
	}
	return renamed
}

// placesAndNames indexes a config's drive and share destinations by where they
// are, and reports every name in use. A place claimed by more than one
// destination is dropped from the index rather than resolved — see
// renamedTargets on why this refuses to guess.
func placesAndNames(cfg *config.Config) (map[string]string, map[string]bool) {
	byPlace := map[string]string{}
	seen := map[string]int{}
	names := map[string]bool{}
	for _, t := range cfg.Targets {
		names[t.Name] = true
		var place string
		switch t.Type {
		case "drive":
			place = "drive\x00" + t.Path
		case "share":
			place = "share\x00" + t.URL + "\x00" + t.Username
		default:
			continue // marks only ever cover drive and share; see prune
		}
		seen[place]++
		byPlace[place] = t.Name
	}
	for place, n := range seen {
		if n > 1 {
			delete(byPlace, place)
		}
	}
	return byPlace, names
}

// renameTarget moves every folder's clocks from one destination name to another,
// in memory.
//
// WHY MEMORY HAS TO BE TOLD AT ALL, when setup.RenameTarget already moves the
// clocks in state.json. Because the flush writes memory (see tally.go), and
// memory is still keyed by the old name until something says otherwise. A flush
// landing between the rename and the config reload therefore writes the old name
// back over the file — and fill cannot put it right afterwards, because fill only
// takes pairs the FILE has that memory does not, and by then the file has the old
// name too. The pair is then lost from both, and every folder reads "never
// synced" to a destination that has been backed up for months. That is what the
// owner saw on 2026-08-11 after renaming a share: the dashboard said nothing was
// backed up on a drive holding 60GB of his files.
//
// NEVER OVER THE TOP OF A CLOCK THE NEW NAME ALREADY HAS. A name that is somehow
// live at both ends of this is not a rename we understand, and a clock that is
// already there is later than one being carried across from a name that has gone.
func (m *syncMarks) renameTarget(from, to string) bool {
	if from == "" || to == "" || from == to {
		return false
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	moved := false
	for _, byTarget := range m.at {
		if when, ok := byTarget[from]; ok {
			if _, taken := byTarget[to]; !taken {
				byTarget[to] = when
				moved = true
			}
			delete(byTarget, from)
		}
	}
	for _, byTarget := range m.scans {
		if mk, ok := byTarget[from]; ok {
			if _, taken := byTarget[to]; !taken {
				byTarget[to] = mk
				moved = true
			}
			delete(byTarget, from)
		}
	}
	return moved
}

// fill takes clocks for folder × destination pairs this process has none for,
// from a state file something else wrote while we were running.
//
// MEMORY STILL WINS WHEREVER BOTH KNOW A PAIR — a state.json written by a setup
// command is behind us, and adopting its timestamp would wind a clock backwards.
// But a pair that exists only on disk is one something else CREATED, and the one
// thing that creates one is a rename: setup.RenameTarget copies a destination's
// clocks over to its new name, and without this the next flush would write them
// straight back out under the name that destination no longer has. Every folder
// would then read "never synced" to a destination that has been backed up for
// months — so a drive nobody has plugged in since would look merely offline
// instead of overdue, which is the exact fault syncMarks exists to prevent.
//
// Reports whether anything was taken, so the caller can flush it back out.
func (m *syncMarks) fill(s *config.State) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	took := false
	for folder, byTarget := range s.MirrorLastSync {
		for target, when := range byTarget {
			if when.IsZero() {
				continue // never is not a mark, the same rule newSyncMarks uses
			}
			if _, known := m.at[folder][target]; known {
				continue
			}
			if m.at[folder] == nil {
				m.at[folder] = map[string]time.Time{}
			}
			m.at[folder][target] = when
			took = true
		}
	}
	for folder, byTarget := range s.MirrorScanState {
		for target, mk := range byTarget {
			if _, known := m.scans[folder][target]; known {
				continue
			}
			if m.scans[folder] == nil {
				m.scans[folder] = map[string]config.ScanMark{}
			}
			m.scans[folder][target] = mk
			took = true
		}
	}
	return took
}

// lastSync reports what to seed an engine with. A pair that has never synced
// gets the zero time, which is what keeps a brand-new destination reading as
// offline rather than as long overdue.
func (m *syncMarks) lastSync(folder, target string) time.Time {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.at[folder][target]
}

// lastSyncMark answers the status collector for a pair that has no engine to
// ask — a paused mirror. The clock is the same one the engine would have been
// seeded with, which is what lets a paused row say when it was last backed up
// instead of "never".
func (d *daemon) lastSyncMark(folderID, target string) time.Time {
	if d.marks == nil {
		return time.Time{}
	}
	return d.marks.lastSync(folderID, target)
}

// record stores one completed sync.
func (m *syncMarks) record(folder, target string, at time.Time) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.at[folder] == nil {
		m.at[folder] = map[string]time.Time{}
	}
	m.at[folder][target] = at
}

// scanMark reports what to seed an engine's scan state with.
//
// A mark learned from DIFFERENT STORAGE under this target name is discarded
// rather than inherited. Target names are just labels in config.toml: swap the
// card in the slot, or point a share name somewhere else, and the name is the
// same while nothing about the destination is. Handing the new storage the old
// one's "everything up to here has been checked" would be a claim nobody made.
func (m *syncMarks) scanMark(folder, target, uuid string) config.ScanMark {
	m.mu.Lock()
	defer m.mu.Unlock()
	mk := m.scans[folder][target]
	if mk.TargetUUID != "" && uuid != "" && mk.TargetUUID != uuid {
		return config.ScanMark{}
	}
	return mk
}

// recordPass stores what one completed pass learned.
func (m *syncMarks) recordPass(folder, target, uuid string, mk localmirror.PassMark) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.scans[folder] == nil {
		m.scans[folder] = map[string]config.ScanMark{}
	}
	trusted := mk.MtimeTrusted
	m.scans[folder][target] = config.ScanMark{
		PassStart:    mk.PassStart,
		MtimeTrusted: &trusted,
		DestFiles:    mk.DestFiles,
		TargetUUID:   uuid,
	}
}

// scanSnapshot copies the scan marks out for writing.
func (m *syncMarks) scanSnapshot() map[string]map[string]config.ScanMark {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.scans) == 0 {
		return nil
	}
	out := make(map[string]map[string]config.ScanMark, len(m.scans))
	for folder, byTarget := range m.scans {
		copied := make(map[string]config.ScanMark, len(byTarget))
		for target, mk := range byTarget {
			copied[target] = mk
		}
		out[folder] = copied
	}
	return out
}

// snapshot copies the marks out for writing. Returns nil when there are none,
// so state.json simply omits the key rather than carrying an empty object.
func (m *syncMarks) snapshot() map[string]map[string]time.Time {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.at) == 0 {
		return nil
	}
	out := make(map[string]map[string]time.Time, len(m.at))
	for folder, byTarget := range m.at {
		copied := make(map[string]time.Time, len(byTarget))
		for target, when := range byTarget {
			copied[target] = when
		}
		out[folder] = copied
	}
	return out
}

// prune drops the marks for folder × destination pairs the config no longer
// has, so removing a folder or a target does not leave its clock behind to grow
// state.json for ever.
//
// It reports whether anything was dropped, so the caller can have the shorter
// file written on the next flush instead of waiting for some unrelated sync to
// make it dirty. The live set comes from the config alone, not from the engines
// actually started: a target that is temporarily missing its recorded UUID is
// still configured, and must not lose its history over it.
func (m *syncMarks) prune(cfg *config.Config) bool {
	live := map[string]map[string]bool{}
	for _, t := range cfg.Targets {
		if t.Type != "drive" && t.Type != "share" {
			continue
		}
		for _, f := range cfg.FoldersForTarget(t) {
			if live[f.ID] == nil {
				live[f.ID] = map[string]bool{}
			}
			live[f.ID][t.Name] = true
		}
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	dropped := false
	for folder, byTarget := range m.at {
		for target := range byTarget {
			if !live[folder][target] {
				delete(byTarget, target)
				dropped = true
			}
		}
		if len(byTarget) == 0 {
			delete(m.at, folder)
		}
	}
	// The scan marks are pruned by the same rule and for the same reason: they
	// are per folder × destination too, and a pair that no longer exists must
	// not keep growing state.json.
	for folder, byTarget := range m.scans {
		for target := range byTarget {
			if !live[folder][target] {
				delete(byTarget, target)
				dropped = true
			}
		}
		if len(byTarget) == 0 {
			delete(m.scans, folder)
		}
	}
	return dropped
}

// syncRecorder is handed to one mirror engine as Options.Synced. Nil-safe on the
// marks and the tally, because engines are also built by tests and by paths that
// have neither.
func (d *daemon) syncRecorder(folderID, target string) func(time.Time) {
	return func(at time.Time) {
		if d.marks == nil {
			return
		}
		d.marks.record(folderID, target, at)
		// Only ever marked dirty here: the tally's flush is what reaches the
		// disk, so a folder syncing every few seconds costs one write per flush
		// interval rather than one per sync.
		d.tally.touch()
	}
}

// passRecorder is handed to one mirror engine as Options.PassCompleted, and is
// nil-safe for the same reason syncRecorder is.
//
// Losing this to a hard kill inside the flush window costs one redundant pass
// and nothing else: the mark only ever suppresses a recopy, so its absence can
// never cause a file to be skipped.
func (d *daemon) passRecorder(folderID, target, uuid string) func(localmirror.PassMark) {
	return func(mk localmirror.PassMark) {
		if d.marks == nil {
			return
		}
		d.marks.recordPass(folderID, target, uuid, mk)
		d.tally.touch()
	}
}
