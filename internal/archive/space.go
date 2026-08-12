// SPDX-License-Identifier: MIT

package archive

import (
	"fmt"
	"io/fs"
	"log/slog"
	"sort"
	"strings"

	"github.com/phil9922/backup-maker/internal/config"
	"github.com/phil9922/backup-maker/internal/localmirror"
)

// minFreeFloorBytes is the room a snapshot leaves spare on a destination that
// has no min_free_gb of its own.
//
// THIS DOES NOT CHANGE WHAT min_free_gb = 0 MEANS. That setting says "never
// delete history to keep the MIRROR running", it is off by default, and the
// Reclaimer stays off here exactly as before. But a snapshot is one file of tens
// of gigabytes written in a single go, and a destination filled to the last byte
// breaks the continuous mirror sharing that drive as surely as it breaks the
// snapshot — the failure takes out the backup that was working.
//
// 2GiB because it is small beside any destination big enough to hold a full
// snapshot at all (so it never turns a run that fits into a run that is
// refused), and it is enough for the mirror beside it to go on copying ordinary
// files instead of failing on a disk with nothing left.
const minFreeFloorBytes = 2 << 30

// growthMargin pads the estimate by 1/8. The estimate is what the LAST run cost
// and the source has been growing since; the padding is deliberately asymmetric
// because the two errors are not comparable. Over-estimating costs one old
// snapshot deleted a day earlier than it strictly had to be. Under-estimating
// fills the destination mid-write, which loses the snapshot AND the mirror.
const growthMargin = 8

// snapshotZip is one finished snapshot of one job on a destination.
type snapshotZip struct {
	path string
	size int64
}

// jobZips lists the finished snapshots of ONE job, oldest first.
//
// dir is <backup-maker-archives>/<machine>/<job> — built by PathFor and by
// nothing else — so every path this returns is a zip THIS job wrote on a
// DESTINATION. That is the one sentence every deletion path here owes: nothing
// outside this job's own directory on the destination can appear in the list, so
// nothing else can be deleted from it. Names are timestamped, so they sort
// oldest first, the same way prune() sorts them.
func jobZips(b localmirror.Backend, dir string) []snapshotZip {
	var out []snapshotZip
	_ = b.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(p, ".zip") {
			return nil
		}
		info, ierr := d.Info()
		if ierr != nil {
			return nil
		}
		out = append(out, snapshotZip{path: p, size: info.Size()})
		return nil
	})
	sort.Slice(out, func(i, j int) bool { return out[i].path < out[j].path })
	return out
}

// estimateSnapshotBytes is how much room this run is likely to need on the
// destination.
//
// THE PREVIOUS ZIP OF THIS JOB, whenever there is one. It is what the last
// identical run actually cost on this same destination — same folders, same
// excludes, same compression — so the compression ratio is already baked into
// it. measure()'s uncompressed source total is not close to it and no fixed
// ratio would get there either: one folder here holds 2.7GB of images beside
// 1.4GB of source, which compress nothing like each other.
//
// The uncompressed total is the fallback for the FIRST run, where there is
// nothing to have learned from. It over-estimates whenever the data compresses
// at all, which is the safe direction to be wrong in.
func estimateSnapshotBytes(cfg *config.Config, job config.Archive, folders []config.Folder, zips []snapshotZip) int64 {
	est := int64(0)
	if n := len(zips); n > 0 {
		est = zips[n-1].size // newest: timestamped names, sorted oldest first
	} else {
		_, est = measure(cfg, job, folders)
	}
	return est + est/growthMargin
}

// ensureRoomFor makes room for the snapshot about to be written, or refuses to
// start it.
//
// Called BEFORE the temp zip is opened, because the alternative is what happens
// today: the writer discovers the destination is full 60GB into a 90GB zip, and
// leaves a part-written temp on a disk with nothing left on it. Returning an
// error here fails the run through archive.Run's normal failure path, which is
// what the "scheduled snapshot failed" alert reports (status gives a job with a
// non-empty Err the state "failed", and alerts.go announces that) — a snapshot
// that cannot run is exactly what that alert is for.
//
// zips and need are worked out by the caller and shared with the spool
// pre-flight beside it (see planSpool): both questions are "how big is this
// snapshot going to be", and measuring the source twice to answer it twice would
// be a second walk of every folder for no gain.
func ensureRoomFor(b localmirror.Backend, cfg *config.Config, job config.Archive,
	zips []snapshotZip, need uint64, log *slog.Logger) error {
	reporter, ok := b.(localmirror.SpaceReporter)
	if !ok {
		// The backend cannot measure space. It then behaves exactly as it did
		// before this check existed — the snapshot is written. Refusing to back
		// up because free space is unknown would stop backups that have been
		// working for months, which is a far worse failure than the one this
		// check exists to prevent.
		return nil
	}
	avail, _, err := reporter.Usage()
	if err != nil {
		log.Warn("could not read free space on the destination; writing the snapshot anyway",
			"archive", job.Name, "err", err)
		return nil
	}

	floor := snapshotFloor(cfg, job)
	want := need + floor
	if avail >= want {
		return nil
	}

	// ONLY THIS JOB'S OWN SNAPSHOTS, and never the last of them.
	//
	// Not the Reclaimer: its round-robin across every job and every folder's
	// version history is right for the mirror's space pressure and wrong here.
	// Somebody set up job A and job B; B quietly losing its history because A
	// grew is not something they asked for. And the floor at one snapshot is the
	// same line reclaim.go draws (`if len(zips) <= 1 { continue }`): a backup
	// program may not leave somebody with none.
	for len(zips) > 1 && avail < want {
		z := zips[0]
		zips = zips[1:]
		if err := b.Remove(z.path); err != nil {
			log.Warn("could not delete an old snapshot to make room for a new one",
				"archive", job.Name, "path", z.path, "err", err)
			continue
		}
		avail += uint64(z.size)
		log.Info("deleted this schedule's oldest snapshot to make room for a new one",
			"archive", job.Name, "path", z.path, "freed_bytes", z.size, "free_bytes", avail)
	}
	if avail >= want {
		return nil
	}
	return fmt.Errorf("%s has not got room for this snapshot: it needs about %s and %s must stay free, but only %s is left%s",
		job.Target, gb(need), gb(floor), gb(avail), keptNote(len(zips)))
}

// keptNote explains the shortfall when there was history that could not be
// touched, so the message does not read as "delete some snapshots" to somebody
// looking at a job that has exactly one.
func keptNote(remaining int) string {
	if remaining == 1 {
		return " (its previous snapshot was kept: a schedule always keeps one)"
	}
	return ""
}

// snapshotFloor resolves how much room to leave spare, preferring what the
// destination was configured with.
func snapshotFloor(cfg *config.Config, job config.Archive) uint64 {
	for _, t := range cfg.Targets {
		if t.Name == job.Target {
			if mf := cfg.MinFreeBytes(t); mf > 0 {
				return mf
			}
			break
		}
	}
	return minFreeFloorBytes
}

// gb renders a size for a message a person reads. Sizes here are always at
// least "a snapshot", so one decimal of a gigabyte is the useful precision.
func gb(n uint64) string {
	return fmt.Sprintf("%.1fGB", float64(n)/(1<<30))
}
