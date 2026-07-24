// SPDX-License-Identifier: MIT

package localmirror

import (
	"io/fs"
	"log/slog"
	"path"
	"sort"
	"strings"
	"sync"
	"time"
)

// ArchivesDirName is where scheduled snapshots live on a destination. It must
// stay identical to archive.DirName; it is duplicated rather than imported
// because internal/archive already depends on this package, and a test in that
// package asserts the two match so they cannot silently drift.
const ArchivesDirName = "backup-maker-archives"

// Reclaimable classifies what the reclaimer is permitted to delete.
//
// THE RULE: the live mirror IS the backup. Deleting from it would leave a
// destination that looks complete but silently isn't, and that would only be
// discovered during a restore — the worst possible moment. So only two things
// are ever eligible:
//
//  1. old file versions under VersionsDirName
//  2. snapshot zips under ArchivesDirName, EXCEPT the newest of each job,
//     because a timed backup whose only snapshot is deleted has no protection
//     left at all
//
// Anything outside those two roots is untouchable, and reclaimable() enforces
// it rather than trusting callers to pass sensible paths.
type reclaimable struct {
	path  string
	group string // folder label or archive job — the unit we spread deletion across
	size  int64
	ts    time.Time
}

// Reclaimer serialises space reclamation for one destination. Several engines
// (one per folder) share a destination, and all of them can hit a full disk at
// the same moment; without this they would each start deleting concurrently
// and race over the same files.
type Reclaimer struct {
	mu       sync.Mutex
	lastRun  time.Time
	lastFree uint64
	lastText string
}

func NewReclaimer() *Reclaimer { return &Reclaimer{} }

// LastOutcome reports the most recent reclaim in human terms, for the
// dashboard. Deleting a user's backup history must never be silent.
func (r *Reclaimer) LastOutcome() (when time.Time, text string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.lastRun, r.lastText
}

// Reclaim deletes the oldest history until at least need bytes have been
// freed, and reports how much it actually managed.
//
// Deletion is spread round-robin across folders: one oldest item per folder
// per pass. Draining a single folder's entire history to protect another's is
// not a decision this tool should make on the user's behalf.
func (r *Reclaimer) Reclaim(b Backend, need uint64, now time.Time, log *slog.Logger) (freed uint64, deleted int) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if need == 0 {
		return 0, 0
	}
	groups := collectReclaimable(b)
	if len(groups) == 0 {
		log.Warn("destination is full and there is no history left to delete",
			"need_bytes", need)
		r.lastRun, r.lastText = now, "full — nothing left that is safe to delete"
		return 0, 0
	}

	// Oldest first within each group.
	names := make([]string, 0, len(groups))
	for name, items := range groups {
		sort.Slice(items, func(i, j int) bool { return items[i].ts.Before(items[j].ts) })
		groups[name] = items
		names = append(names, name)
	}
	sort.Strings(names) // deterministic order for tests and logs

	cursor := map[string]int{}
	for freed < need {
		progressed := false
		for _, name := range names {
			if freed >= need {
				break
			}
			i := cursor[name]
			if i >= len(groups[name]) {
				continue
			}
			cursor[name] = i + 1
			item := groups[name][i]
			if err := b.Remove(item.path); err != nil {
				log.Warn("could not delete to reclaim space", "path", item.path, "err", err)
				continue
			}
			freed += uint64(item.size)
			deleted++
			progressed = true
			log.Info("deleted old backup history to make room",
				"path", item.path, "folder", name,
				"bytes", item.size, "age", now.Sub(item.ts).Round(time.Hour))
		}
		if !progressed {
			break // every group exhausted
		}
	}

	removeEmptyDirs(b, VersionsDirName)
	r.lastRun = now
	if deleted > 0 {
		r.lastText = humanReclaim(freed, deleted)
		log.Info("reclaimed space", "bytes", freed, "files", deleted)
	} else {
		r.lastText = "full — nothing left that is safe to delete"
	}
	return freed, deleted
}

func humanReclaim(freed uint64, deleted int) string {
	unit, div := "B", uint64(1)
	switch {
	case freed >= 1<<30:
		unit, div = "GB", 1<<30
	case freed >= 1<<20:
		unit, div = "MB", 1<<20
	case freed >= 1<<10:
		unit, div = "KB", 1<<10
	}
	plural := "s"
	if deleted == 1 {
		plural = ""
	}
	return "freed " + itoa(freed/div) + unit + " by deleting " +
		itoa(uint64(deleted)) + " old backup file" + plural
}

func itoa(v uint64) string {
	if v == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	for v > 0 {
		i--
		b[i] = byte('0' + v%10)
		v /= 10
	}
	return string(b[i:])
}

// collectReclaimable walks ONLY the two deletable roots and groups what it
// finds. Nothing outside them is ever returned, so the live mirror cannot be
// reached even by a caller mistake.
func collectReclaimable(b Backend) map[string][]reclaimable {
	groups := map[string][]reclaimable{}

	// 1. Old file versions. versionPath() writes them under
	// VersionsDirName/<machine>/<label>/..., so the label is the folder.
	_ = b.WalkDir(VersionsDirName, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		m := stampRe.FindStringSubmatch(d.Name())
		if m == nil {
			return nil
		}
		ts, perr := time.ParseInLocation(stampLayout, m[1], time.Local)
		if perr != nil {
			return nil
		}
		info, ierr := d.Info()
		if ierr != nil {
			return nil
		}
		groups[folderOf(p)] = append(groups[folderOf(p)], reclaimable{
			path: p, group: folderOf(p), size: info.Size(), ts: ts,
		})
		return nil
	})

	// 2. Snapshot zips, minus the newest of each job.
	byJob := map[string][]reclaimable{}
	_ = b.WalkDir(ArchivesDirName, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(d.Name(), ".zip") {
			return nil
		}
		info, ierr := d.Info()
		if ierr != nil {
			return nil
		}
		job := path.Dir(p)
		byJob[job] = append(byJob[job], reclaimable{
			path: p, group: job, size: info.Size(), ts: info.ModTime(),
		})
		return nil
	})
	for job, zips := range byJob {
		if len(zips) <= 1 {
			continue // never take a job's only snapshot
		}
		sort.Slice(zips, func(i, j int) bool { return zips[i].ts.After(zips[j].ts) })
		groups[job] = append(groups[job], zips[1:]...) // all but the newest
	}
	return groups
}

// folderOf extracts the folder label from a version path
// (.backup-maker-versions/<machine>/<label>/...), so deletion can be spread
// evenly across the folders being protected.
func folderOf(versionPath string) string {
	rest := strings.TrimPrefix(versionPath, VersionsDirName+"/")
	parts := strings.SplitN(rest, "/", 3)
	if len(parts) >= 2 {
		return parts[0] + "/" + parts[1]
	}
	return rest
}
