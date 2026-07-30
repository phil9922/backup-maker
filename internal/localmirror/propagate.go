// SPDX-License-Identifier: MIT

package localmirror

import (
	"errors"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"time"
)

// maxFastPathFiles is how many changed files the fast path will handle before it
// gives up and lets a full pass do the work instead.
//
// The crossover is far higher than this — a full pass costs one round trip per
// DIRECTORY (9,399 of them on a real tree) and the fast path costs about two per
// file — so on arithmetic alone a batch of a couple of thousand would still be
// cheaper. It is deliberately low anyway, because the fast path exists to make a
// SAVED FILE arrive quickly, and a burst of two hundred changes is not somebody
// saving a file: it is a build, a checkout, or an unpack. Those want the full
// pass's ordering, progress bar and tidy-up, and they can wait a minute for it.
const maxFastPathFiles = 200

// propagate copies a handful of changed files and does nothing else.
//
// WHY THIS EXISTS. A pass spends nearly all of its time enumerating the
// destination — 26 seconds of 27.5 on a real share, because there are 9,399
// directories and each is a round trip. Every one of those seconds sat between
// somebody pressing save and their file existing anywhere else, which is the one
// number the front page makes a promise about ("copied within seconds of you
// saving"). This path answers that promise; the full pass keeps every guarantee.
//
// WHAT IT DELIBERATELY CANNOT DO, and this is the whole reason it is safe:
//
//   - It NEVER versions anything away and NEVER removes anything. It only ever
//     writes files that exist in the source right now. A deletion seen by the
//     watcher is ignored here and handled by the next full pass, because
//     versioning away is a decision the safety guards make against a COMPLETE
//     index of the destination, and this path has no such index. A wrong decision
//     here would be invisible and irreversible; a late one costs an hour.
//   - It records no state that a later pass will trust. In particular it does not
//     write a PassMark: that mark means "everything up to here has been checked",
//     and this checked a handful of files. Claiming otherwise would let the next
//     pass skip work on the strength of a promise nobody made.
//   - Its only failure mode is copying a file it did not need to, which costs
//     bandwidth and nothing else.
//
// So the worst thing a bug in here can do is waste a round trip. Everything that
// can lose a backup stays in reconcile, unchanged, with its full index.
func (e *Engine) propagate(rels []string) (copied int) {
	if !e.readyToWrite() {
		return 0
	}
	now := time.Now()
	sort.Strings(rels) // deterministic, like the full pass

	// "syncing" rather than "scanning": there is nothing to scan, and the state
	// column answers whether the files are safe — which, on a folder that has
	// completed a pass before, they are.
	e.setState("syncing")
	for _, rel := range rels {
		info, ok := e.fastPathCandidate(rel)
		if !ok {
			continue
		}
		if !e.shouldCopyIndexed(info, rel, e.destEntryFor(rel)) {
			continue
		}
		// One file at a time, so the bar is honest about a batch this small.
		e.beginTransfer(1, info.Size())
		if err := e.copyOne(rel, info.Size(), now); err != nil {
			e.endTransfer()
			if IsNoSpace(err) {
				// Out of room is not a fast-path problem to solve. Stop and let
				// the full pass reclaim space with the whole picture in front of it.
				e.log.Warn("destination is full; leaving it to the next full pass", "err", err)
				break
			}
			continue
		}
		e.endTransfer()
		copied++
	}
	e.endTransfer()

	if copied > 0 {
		// lastSync, but NOT a PassMark. The distinction is the point: this
		// destination has just been written to and confirmed, which is what the
		// "last sync" column reports and what staleness detection is asking
		// about. What has NOT happened is a verification of the whole tree, and
		// that is what a PassMark claims — so the next full pass still does all
		// of its work.
		e.noteSynced(now)
		e.log.Info("copied a change without a full pass", "copied", copied,
			"considered", len(rels))
	}
	e.mu.Lock()
	e.state = "in sync"
	e.mu.Unlock()
	e.endScan()
	return copied
}

// fastPathCandidate reports whether rel is a file this path may copy, applying
// the same rules the full pass's source walk applies — excluded paths, symlinks,
// and anything that is not a regular file.
//
// A path that has vanished is NOT a candidate and NOT an error: the watcher fires
// for deletions too, and a deletion is the full pass's business.
func (e *Engine) fastPathCandidate(rel string) (os.FileInfo, bool) {
	if rel == "" || rel == "." || e.ignore.Ignored(rel) {
		return nil, false
	}
	src := filepath.Join(e.sourcePath, filepath.FromSlash(rel))
	info, err := os.Lstat(src)
	if err != nil {
		return nil, false // gone, or unreadable: the full pass will deal with it
	}
	if info.Mode()&fs.ModeSymlink != 0 || info.IsDir() || !info.Mode().IsRegular() {
		return nil, false
	}
	return info, true
}

// destEntryFor asks the destination about one path and returns it as a one-entry
// index.
//
// A ONE-ENTRY INDEX RATHER THAN A SECOND COMPARISON. shouldCopyIndexed carries
// rules that took real failures to arrive at — case-insensitive destinations, the
// mtime tolerance, and the fallback to "changed since the last completed scan" on
// a destination that does not preserve timestamps. Handing it an index of one is
// what makes the fast path's decision the SAME CODE as the full pass's, rather
// than a copy that agrees today.
func (e *Engine) destEntryFor(rel string) *destIndex {
	idx := &destIndex{byRel: make(map[string]destEntry, 1)}
	fi, err := e.backend.Stat(path.Join(e.destRoot, rel))
	if err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			// Anything other than "not there" is a destination problem, not an
			// answer. An empty index means "copy it", which is the safe way to be
			// wrong: worst case the file is sent again.
			e.log.Debug("could not ask the destination about a changed file",
				"path", rel, "err", err)
		}
		return idx
	}
	if fi.IsDir() {
		return idx
	}
	idx.byRel[rel] = destEntry{size: fi.Size(), mtime: fi.ModTime()}
	return idx
}

// fastPathUsable reports whether a batch of changed paths may take the fast path.
//
// Two refusals, both about not making a promise this path cannot keep:
//
//   - NO COMPLETED PASS YET. A first backup has nothing on the destination to
//     compare against, no PassMark, and a dashboard that says "first backup
//     running". Copying a few files into that would report progress against a
//     total nobody has counted. prevScanStart is the honest test: it is set only
//     at the very end of a reconcile that ran to completion, and seeded from the
//     persisted mark, so it survives a restart.
//   - TOO MANY FILES. See maxFastPathFiles: a burst that size is a build, not
//     somebody saving, and it wants the full pass.
func (e *Engine) fastPathUsable(rels []string) bool {
	if len(rels) == 0 || len(rels) > maxFastPathFiles {
		return false
	}
	return !e.prevScanStart.IsZero()
}
