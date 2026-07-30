// SPDX-License-Identifier: MIT

package localmirror

import (
	"errors"
	"io/fs"
	"os"
	"path"
	"sort"
	"strings"
	"sync"
	"time"
)

// destEntry is what the destination already holds for one file: the only two
// facts the copy decision compares. Deliberately not an fs.FileInfo, because a
// backend's FileInfo can retain the server's whole reply and there is one of
// these per mirrored file.
type destEntry struct {
	size  int64
	mtime time.Time
}

// destIndex is the mirror root as it stood at the start of this pass.
//
// WHY A WALK AND NOT A STAT PER FILE. Deciding what to copy used to cost one
// backend.Stat per source file. Over SMB each of those is a round trip — open,
// query, close — and on a folder of 70,821 files that measured 8 to 15 minutes
// of latency before a single byte could move. The pass was thrown away whole on
// any interruption, so on a machine being restarted through the day it never
// once ran to the end: the destination held a complete copy for two days while
// the dashboard said "never".
//
// A directory listing returns the size and mtime of every entry in it in one
// reply — go-smb2's DirFS hands back the FileInfo the directory query already
// carried, with no further round trip (dirInfo.Info in client_fs.go). So the
// same information costs one round trip per DIRECTORY instead of three per
// file. reconcile already walked this tree once for its cleanup pass, so this
// asks nothing new of the destination.
//
// READ-ONLY, AND THAT IS LOAD-BEARING. Building this touches no deletion path.
// Stale temp files and empty directories are still handled by the cleanup walk
// at the end of reconcile, under the guards it already has.
type destIndex struct {
	byRel map[string]destEntry

	// folded is the case-insensitive fallback, built on first miss.
	//
	// SMB and macOS destinations are case-insensitive, and backend.Stat was too:
	// asking for "Foo.txt" on a server holding "foo.txt" found it, and the file
	// was left alone. An exact-match map alone would miss, and every pass would
	// recopy the file — the forever-recopy failure calibrateMtime exists to
	// avoid, in a different disguise.
	//
	// Keys that two destination files share are dropped rather than guessed:
	// on a case-SENSITIVE destination "Foo.txt" and "foo.txt" are two different
	// files, and answering for one with the other's size would be worse than
	// not answering at all.
	folded    map[string]destEntry
	ambiguous map[string]bool
	built     bool

	// dirs is every directory under the mirror root, and tempAt every abandoned
	// .bmtmp with the time it was last written. Both are collected by the SAME
	// walk that builds byRel.
	//
	// The pass used to walk this tree three times: once to index it, once to
	// look for files deleted from the source, and once to clear the directories
	// they left empty. Over SMB against 72,000 files that was three quarters of
	// the pass — and the two later walks were asking questions this one already
	// had the answers to.
	//
	// WHAT THIS DOES NOT CHANGE: which paths may be removed. Everything here was
	// already proved to be under the mirror root by relTo, the version store and
	// the marker are excluded before anything is recorded, and the rules applied
	// to these lists are the rules the walks applied. A destination file that
	// appeared AFTER this index was built is simply absent from it, so it is
	// left alone this pass and reconsidered on the next — erring towards keeping
	// a file rather than removing one.
	dirs   []string
	tempAt []tempFile
}

// tempFile is an abandoned .bmtmp and when it was last written. The age test
// lives where the removing happens, not here: this index is read-only.
type tempFile struct {
	rel string
	mod time.Time
}

// buildDestIndex lists the whole mirror root.
//
// An error anywhere in the walk fails the pass rather than returning a partial
// index. A missing entry here means "copy it", so a half-built index would
// recopy most of the folder — and a destination that dropped mid-walk is
// exactly what the engine already reports as offline and retries.
//
// A mirror root that does not exist yet is not an error. That is a first
// backup, and the honest index for it is the empty one.
func (e *Engine) buildDestIndex() (*destIndex, error) {
	idx := &destIndex{byRel: make(map[string]destEntry)}

	if _, err := e.backend.Stat(e.destRoot); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return idx, nil
		}
		return nil, err
	}

	// On a share, list several directories at once: each listing is a round
	// trip that waits on nothing else, and there are thousands of them. A local
	// drive keeps the sequential walk — a ReadDir there is a syscall, so
	// parallelism buys nothing and only adds scheduling to an engine that is
	// already the CPU cost on this machine.
	if dl, ok := e.backend.(DirLister); ok && e.TargetType == "share" {
		out, err := e.buildDestIndexParallel(dl, idx, dirListWorkers)
		if out != nil {
			e.dirsListed = len(out.dirs) + 1 // +1 for the root itself
		}
		return out, err
	}

	var walkErr error
	err := e.backend.WalkDir(e.destRoot, func(p string, d fs.DirEntry, perr error) error {
		if perr != nil {
			// Unlike the cleanup walk, which skips what it cannot read because
			// its job is to delete and skipping is the safe direction, an entry
			// missed here reads as "not on the destination" and would be
			// recopied. Stop and let the pass be retried.
			walkErr = perr
			return perr
		}
		rel, under := relTo(e.destRoot, p)
		if !under || rel == "" || rel == "." {
			return nil
		}
		if isEngineArtifact(rel, d.Name()) {
			if d.IsDir() {
				return fs.SkipDir
			}
			// A temp file from an interrupted copy. Recorded, never removed
			// here: building the index writes nothing.
			if strings.HasSuffix(d.Name(), tmpSuffix) {
				if info, ierr := d.Info(); ierr == nil {
					idx.tempAt = append(idx.tempAt, tempFile{rel: rel, mod: info.ModTime()})
				}
			}
			return nil
		}
		if d.IsDir() {
			idx.dirs = append(idx.dirs, rel)
			return nil
		}
		if !d.Type().IsRegular() {
			return nil
		}
		info, ierr := d.Info()
		if ierr != nil {
			walkErr = ierr
			return ierr
		}
		idx.byRel[rel] = destEntry{size: info.Size(), mtime: info.ModTime()}
		e.scanned.Add(1)
		return nil
	})
	if err != nil {
		return nil, err
	}
	if walkErr != nil {
		return nil, walkErr
	}
	return idx, nil
}

// dirListWorkers is how many directory listings a share is asked for at once.
//
// EIGHT, AND IT IS A CEILING, NOT A GUESS. Measured against the real Pi over wifi,
// listing the same 4,178 directories at each level
// (TestReadDirConcurrencyScaling in internal/smbfs):
//
//	workers=1   1m1.9s   14.81ms/dir   failed=0
//	workers=4    23.0s    5.50ms/dir   failed=0     2.7x
//	workers=8    11.8s    2.81ms/dir   failed=0     5.3x
//	workers=12    7.2s    1.73ms/dir   failed=57    ← refused
//	workers=16    5.7s    1.35ms/dir   failed=106   ← refused
//	workers=24    4.1s    0.98ms/dir   failed=332   ← refused
//	workers=32    3.4s    0.80ms/dir   failed=188   ← refused
//
// Past eight the server starts answering "An invalid parameter was passed to a
// service or function" (STATUS_INVALID_PARAMETER), and a refused listing does not
// degrade the pass — it FAILS it, because a directory missing from the index reads
// as "not on the destination" and would recopy the tree. Backing up to that machine
// stops until the number comes back down. Verified the hard way: setting this to 32
// on the strength of a benchmark that discarded errors took the Pi's backups down
// until it was reverted.
//
// So this is the fastest safe value, and 12 is already past the edge. DO NOT RAISE
// IT without re-running that test against a real destination and reading the
// failed= column — the timings alone look like an argument for 32.
//
// It matters because the index is where a pass spends its time: 26s of a 27.5s pass
// over 9,399 directories, at ~2.8ms each, which is exactly the 8-worker rate above.
// The parallelism is working; that cost is what eight concurrent round trips to
// this destination buys.
//
// WHERE THE REMAINING TIME COULD GO, since concurrency cannot take it: list fewer
// directories, by not re-enumerating an unchanged tree every pass. That is a real
// change with a real hazard — this index decides what gets versioned away, so a
// stale one could remove a file that is actually there. It needs an invalidation
// story before a line of it is written, not an optimisation bolted on.
//
// The level-by-level barrier below was measured and left alone: with eight workers
// it costs about 1% against a perfect work queue on this tree, because nearly every
// level is far wider than the worker count.
const dirListWorkers = 8

// buildDestIndexParallel lists the tree a level at a time, several directories
// at once.
//
// Level by level rather than a work-stealing queue because it is the version
// whose termination is obvious: each level is a closed set of directories, and
// the next level is exactly what they contain. A backup tree is wide and
// shallow, so every level below the first has ample work to spread.
//
// STILL READ-ONLY, AND STILL FAILS THE WHOLE PASS ON ANY ERROR. A listing that
// silently came back short would read as "these files are not on the
// destination" and recopy them; several workers hitting a dropped connection
// all land on the same answer, which is to abandon the pass and let the offline
// poll retry it.
func (e *Engine) buildDestIndexParallel(dl DirLister, idx *destIndex, workers int) (*destIndex, error) {
	var mu sync.Mutex // guards idx and next
	level := []string{e.destRoot}

	for len(level) > 0 {
		var next []string
		var firstErr error

		sem := make(chan struct{}, workers)
		var wg sync.WaitGroup
		for _, dir := range level {
			wg.Add(1)
			sem <- struct{}{}
			go func(dir string) {
				defer wg.Done()
				defer func() { <-sem }()

				ents, err := dl.ReadDir(dir)
				mu.Lock()
				defer mu.Unlock()
				if err != nil {
					if firstErr == nil {
						firstErr = err
					}
					return
				}
				for _, d := range ents {
					p := path.Join(dir, d.Name())
					rel, under := relTo(e.destRoot, p)
					if !under || rel == "" || rel == "." {
						continue
					}
					if isEngineArtifact(rel, d.Name()) {
						if !d.IsDir() && strings.HasSuffix(d.Name(), tmpSuffix) {
							if info, ierr := d.Info(); ierr == nil {
								idx.tempAt = append(idx.tempAt, tempFile{rel: rel, mod: info.ModTime()})
							}
						}
						continue
					}
					if d.IsDir() {
						idx.dirs = append(idx.dirs, rel)
						next = append(next, p)
						continue
					}
					if !d.Type().IsRegular() {
						continue
					}
					info, ierr := d.Info()
					if ierr != nil {
						if firstErr == nil {
							firstErr = ierr
						}
						return
					}
					idx.byRel[rel] = destEntry{size: info.Size(), mtime: info.ModTime()}
					e.scanned.Add(1)
				}
			}(dir)
		}
		wg.Wait()
		if firstErr != nil {
			return nil, firstErr
		}
		// Deterministic order, so a log or a test reads the same twice.
		sort.Strings(next)
		level = next
	}
	return idx, nil
}

// staleBefore returns the temp files last written before cutoff — the ones a
// crash or a dropped connection stranded, rather than one a copy is writing
// right now.
func (d *destIndex) staleBefore(cutoff time.Time) []string {
	var out []string
	for _, t := range d.tempAt {
		if t.mod.Before(cutoff) {
			out = append(out, t.rel)
		}
	}
	return out
}

// lookup reports what the destination holds for rel.
func (d *destIndex) lookup(rel string) (destEntry, bool) {
	if e, ok := d.byRel[rel]; ok {
		return e, true
	}
	if !d.built {
		d.buildFolded()
	}
	key := strings.ToLower(rel)
	if d.ambiguous[key] {
		return destEntry{}, false
	}
	e, ok := d.folded[key]
	return e, ok
}

func (d *destIndex) buildFolded() {
	d.built = true
	d.folded = make(map[string]destEntry, len(d.byRel))
	d.ambiguous = make(map[string]bool)
	for rel, e := range d.byRel {
		key := strings.ToLower(rel)
		if _, clash := d.folded[key]; clash {
			d.ambiguous[key] = true
			continue
		}
		d.folded[key] = e
	}
}

// shouldCopyIndexed answers shouldCopy from the index instead of a live Stat.
//
// The same three questions in the same order as shouldCopy and needsCopy, so a
// change to one has to be a change to all of them. Kept beside them rather than
// folded in, because shouldCopy is still the single-file path.
func (e *Engine) shouldCopyIndexed(srcInfo os.FileInfo, rel string, idx *destIndex) bool {
	e.scanned.Add(1)
	di, ok := idx.lookup(rel)
	if !ok {
		return true // not on the destination
	}
	if di.size != srcInfo.Size() {
		return true
	}
	if !e.mtimeTrusted {
		// Same size on a destination that does not preserve timestamps: recopy
		// only if the source changed since the last completed scan.
		return srcInfo.ModTime().After(e.prevScanStart)
	}
	delta := srcInfo.ModTime().Sub(di.mtime)
	if delta < 0 {
		delta = -delta
	}
	return delta > mtimeTolerance
}
