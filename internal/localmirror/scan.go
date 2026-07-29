// SPDX-License-Identifier: MIT

package localmirror

import (
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// staleTempAge is how old an orphaned .bmtmp file must be before the
// reconciler deletes it (a crash or dropped connection can strand them).
const staleTempAge = time.Hour

// reconcile performs a full one-way reconciliation pass: copy every new or
// changed source file to the destination, and version away anything on the
// destination that no longer exists in the source. It is the correctness
// backstop for missed watcher events and the catch-up path after a target
// returns. Returns (files copied, files versioned away).
func (e *Engine) reconcile() (copied, removed int, err error) {
	now := time.Now()
	sourceFiles := map[string]os.FileInfo{}

	// Before anything moves: say so if a release that predates the exclusion
	// already put our configuration directory here.
	e.warnAboutCopiedConfig()

	// No denominator for the source walk: counting the folder first, only to
	// then walk it, would double its cost for a number nobody waits on.
	e.beginScanPhase("source", 0)
	err = filepath.WalkDir(e.sourcePath, func(p string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			e.addFileError(p, walkErr)
			return nil // keep going; one unreadable entry must not stop a backup
		}
		rel, rerr := filepath.Rel(e.sourcePath, p)
		if rerr != nil || rel == "." {
			return nil
		}
		rel = filepath.ToSlash(rel)
		if e.ignore.Ignored(rel) {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if d.Type()&fs.ModeSymlink != 0 {
			e.noteSymlinkSkipped(rel)
			return nil
		}
		if d.IsDir() {
			return nil
		}
		if !d.Type().IsRegular() {
			return nil // sockets, fifos, devices: not backup material
		}
		info, ierr := d.Info()
		if ierr != nil {
			e.addFileError(p, ierr)
			return nil
		}
		sourceFiles[rel] = info
		e.scanned.Add(1)
		return nil
	})
	if err != nil {
		return 0, 0, err
	}

	// Deterministic copy order (helpful for logs and resumability).
	rels := make([]string, 0, len(sourceFiles))
	for rel := range sourceFiles {
		rels = append(rels, rel)
	}
	sort.Strings(rels)

	// Decide the whole pending set before copying anything. This costs exactly
	// the same I/O as testing each file inline (one shouldCopy per file either
	// way — it is not a second pass over the destination), but it yields a real
	// denominator, so the dashboard can show true progress instead of a
	// spinner. Without it there is no honest way to say "412MB of 2.9GB".
	//
	// The destination is listed once, up front, rather than asked about each
	// file in turn: see destIndex. On a share that is the difference between
	// one round trip per directory and three per file, which is where the
	// minutes of a pass were going.
	// Counted against what the last completed pass found there, which is exact
	// once one has ever landed and honestly absent before that.
	e.beginScanPhase("listing", e.destFileHint)
	idx, err := e.buildDestIndex()
	if err != nil {
		return 0, 0, err
	}
	e.beginScanPhase("comparing", int64(len(rels)))
	pending := make([]string, 0, len(rels))
	var pendingBytes int64
	for _, rel := range rels {
		if !e.shouldCopyIndexed(sourceFiles[rel], rel, idx) {
			continue
		}
		pending = append(pending, rel)
		pendingBytes += sourceFiles[rel].Size()
	}

	e.beginTransfer(len(pending), pendingBytes)
	defer e.endTransfer()

	for _, rel := range pending {
		destPath := path.Join(e.destRoot, rel)
		src := filepath.Join(e.sourcePath, filepath.FromSlash(rel))
		written, err := copyFile(e.backend, src, destPath, now, e.verify, e.reportInFlight)
		if IsNoSpace(err) {
			// The destination filled up mid-pass. Free room for this file
			// specifically, then give it one more go.
			if e.ensureHeadroom(uint64(sourceFiles[rel].Size())) {
				written, err = copyFile(e.backend, src, destPath, now, e.verify, e.reportInFlight)
			}
			if IsNoSpace(err) {
				// Still full: stop the pass rather than grinding through
				// every remaining file failing identically.
				e.advanceTransfer(sourceFiles[rel].Size())
				return copied, removed, err
			}
		}
		if err != nil {
			e.addFileError(rel, err)
			// Still count it as handled, or a failing file would freeze the
			// bar short of 100% for the rest of the pass.
			e.advanceTransfer(sourceFiles[rel].Size())
			continue
		}
		e.advanceTransfer(sourceFiles[rel].Size())
		// Only here, past every failure branch above: the file is on the
		// destination and the odometer may claim it.
		e.noteCopied(written)
		copied++
	}

	// Anything mirrored that's gone from (or now ignored in) the source gets
	// versioned away; stale temp files from interrupted copies are dropped.
	//
	// READ FROM THE INDEX BUILT AT THE TOP OF THIS PASS, not from a fresh walk.
	// The index already visited every file and every directory here, so walking
	// again asked the destination questions it had just answered — over SMB that
	// second walk, plus the directory sweep below, was most of the pass.
	//
	// EVERY GUARD STILL APPLIES, and none of them moved:
	//   - relTo proved each of these paths is under destRoot when the index was
	//     built; the full path is rebuilt from that same validated rel.
	//   - the version store, the marker and in-flight temps are excluded from
	//     byRel by isEngineArtifact before anything is recorded.
	//   - a file is versioned away only when it is absent from sourceFiles,
	//     which is the rule the walk used.
	// The one behavioural difference is a file that appeared on the destination
	// AFTER the index was built: it is not in the index, so it survives this
	// pass and is reconsidered on the next. That errs towards keeping a file,
	// which is the only direction this program is allowed to err in.
	e.beginScanPhase("tidying", int64(len(idx.byRel)))
	for rel := range idx.byRel {
		e.scanned.Add(1)
		if _, ok := sourceFiles[rel]; ok {
			continue
		}
		if err := keepVersion(e.backend, path.Join(e.destRoot, rel), now); err != nil {
			e.addFileError(rel, err)
			continue
		}
		removed++
	}
	// Temps older than an hour are the leftovers of a copy that died; a younger
	// one may belong to a copy running right now.
	for _, rel := range idx.staleBefore(now.Add(-staleTempAge)) {
		_ = e.backend.Remove(path.Join(e.destRoot, rel))
	}

	// Timed on its own though it stays in the "tidying" phase on screen: it is
	// the same activity to a reader and a separate cost to anyone asking where a
	// slow pass went. It sweeps directories over the network one refusal at a
	// time, so it is the first place to look.
	e.beginStage("dirs")
	e.removeEmptyDestDirs(idx)
	e.prevScanStart = now // only read/written from the sync goroutine
	// Past every failure return above: this pass reached the end, so what it
	// learned is worth carrying across a restart. An interrupted pass gets here
	// never, which is the whole guarantee.
	e.notePassCompleted(PassMark{
		PassStart:    now,
		MtimeTrusted: e.mtimeTrusted,
		DestFiles:    int64(len(idx.byRel)),
	})
	return copied, removed, nil
}

// warnAboutCopiedConfig reports a copy of backup-maker's own configuration
// directory sitting on this destination — left by a release that mirrored it
// before the exclusion existed. It contains this machine's share and archive
// passwords, so it has to be said out loud rather than quietly stopped.
//
// Nothing is deleted: this engine never removes anything from a mirror, and
// what this pass displaces goes to the version store, still on the destination.
// Only the user can decide to destroy it, and only they can rotate what it
// exposed.
//
// Said once per destination, on the transition — the same rate limit the daemon
// puts on the space-unknown warning. Repeating it every scan is how a warning
// the user has already acted on turns into noise they stop reading.
func (e *Engine) warnAboutCopiedConfig() {
	if len(e.ignore.anchored) == 0 {
		return
	}
	e.mu.Lock()
	warned := e.selfCopyWarned
	e.mu.Unlock()
	if warned {
		return
	}
	for _, rel := range e.ignore.anchored {
		leaked := path.Join(e.destRoot, rel)
		if _, serr := e.backend.Stat(leaked); serr != nil {
			continue
		}
		e.mu.Lock()
		e.selfCopyWarned = true
		e.mu.Unlock()
		e.log.Warn("this destination holds a copy of backup-maker's own configuration folder from an earlier version: it exposes this machine's share and archive passwords (the ones that decrypt the snapshots) and its sync identity. Delete that path on the destination yourself — backup-maker never deletes from a mirror — and change every password it held",
			"target", e.TargetName, "path", leaked,
			"also_under", path.Join(VersionsDirName, leaked))
		return
	}
}

// relTo returns p as a path relative to root, and false when p is not inside
// root at all.
//
// ANCHORED AT A SEPARATOR, which is the whole point. A plain prefix test calls
// "<root>-old" a child of "<root>", and this package has already paid for that
// once: EnforceReceiveOnly matched a sibling directory that way and would have
// forced one of the user's own folders receive-only. Here the stakes are the
// same shape — the callers version files away and delete empty directories, and
// isEngineArtifact decides whether something is the version store by looking at
// the first segment of the value returned here.
//
// Both backends walk a genuinely rooted tree, so today every path arrives under
// root and the false case never fires. It is written to fail closed anyway: a
// backend that one day yields a stray path should cost nothing, rather than
// silently aiming a delete at whatever the mis-derived name resolves to.
func relTo(root, p string) (string, bool) {
	q := path.Clean(p)
	r := path.Clean(root)
	if r == "" || r == "." || r == "/" {
		return strings.TrimPrefix(q, "/"), true
	}
	if q == r {
		return "", true
	}
	if !strings.HasPrefix(q, r+"/") {
		return "", false
	}
	return q[len(r)+1:], true
}

// isEngineArtifact reports paths the reconciler must never treat as user
// data: the version store, the target marker, and in-flight temp files.
// rel is relative to the mirror root (destRoot).
func isEngineArtifact(rel, base string) bool {
	top := rel
	if i := strings.IndexByte(rel, '/'); i > 0 {
		top = rel[:i]
	}
	return top == VersionsDirName || rel == MarkerName || strings.HasSuffix(base, tmpSuffix)
}

// removeEmptyDestDirs drops empty directories left behind by deletions,
// while keeping the mirror root and version store.
func (e *Engine) removeEmptyDestDirs(idx *destIndex) {
	// From the index rather than a third walk of the same tree. The directories
	// are the ones it recorded on its way through, already proved to be under
	// the mirror root by relTo and already excluding the version store.
	//
	// Deepest first, and Remove is expected to fail on anything that still has
	// something in it — that is how a directory emptied by removing its only
	// child gets taken too, and why the error is ignored. A directory created
	// during this pass is absent from the index, but it holds the file that was
	// just copied into it, so it is not empty and would not be removed anyway.
	// ONLY THE ONES THAT COULD BE EMPTY. This asked the destination to remove
	// every directory it had, deepest first, and relied on the failures: a
	// directory holding files cannot be removed, so the error was ignored. On a
	// local drive that is thousands of cheap syscalls. Over SMB it is thousands
	// of round trips per pass, all of them refusals — and once the two extra
	// walks were gone, it was what the tidy phase spent its time on.
	//
	// A directory that holds a file, or that contains one further down, is not
	// empty and never will be during this pass. The index knows every file, so
	// their parents are known too, and everything else is a candidate.
	occupied := make(map[string]bool, len(idx.byRel))
	for rel := range idx.byRel {
		for d := path.Dir(rel); d != "." && d != "/" && d != ""; d = path.Dir(d) {
			if occupied[d] {
				break // this ancestor and all above it are already marked
			}
			occupied[d] = true
		}
	}
	dirs := append([]string(nil), idx.dirs...)
	sort.Strings(dirs)
	for i := len(dirs) - 1; i >= 0; i-- { // deepest first
		if occupied[dirs[i]] {
			continue
		}
		e.scanned.Add(1)
		// Still expected to fail sometimes — a directory holding only a temp
		// file looks empty from here — and the error is still ignored. A
		// directory emptied by THIS pass keeps its files in the index, so it is
		// cleared on the next one rather than this one.
		_ = e.backend.Remove(path.Join(e.destRoot, dirs[i]))
	}
}
