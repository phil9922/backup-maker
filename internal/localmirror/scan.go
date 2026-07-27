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
	pending := make([]string, 0, len(rels))
	var pendingBytes int64
	for _, rel := range rels {
		if !e.shouldCopy(sourceFiles[rel], path.Join(e.destRoot, rel)) {
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
	err = e.backend.WalkDir(e.destRoot, func(p string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return nil
		}
		rel, under := relTo(e.destRoot, p)
		if !under {
			// A path the walk should never have produced. Everything below
			// this line either versions a file away or deletes it, and the
			// guard that protects the version store and the marker works on
			// rel — so a rel we could not derive honestly would aim those
			// actions at the wrong file. Skipping is the only safe answer.
			return nil
		}
		if rel == "" || rel == "." || p == e.destRoot {
			return nil
		}
		if isEngineArtifact(rel, d.Name()) {
			if d.IsDir() {
				return fs.SkipDir
			}
			if strings.HasSuffix(d.Name(), tmpSuffix) {
				if info, ierr := d.Info(); ierr == nil && now.Sub(info.ModTime()) > staleTempAge {
					_ = e.backend.Remove(p)
				}
			}
			return nil
		}
		if d.IsDir() {
			return nil
		}
		if _, ok := sourceFiles[rel]; ok {
			return nil
		}
		if err := keepVersion(e.backend, p, now); err != nil {
			e.addFileError(rel, err)
			return nil
		}
		removed++
		return nil
	})
	if err != nil {
		return copied, removed, err
	}

	e.removeEmptyDestDirs()
	e.prevScanStart = now // only read/written from the sync goroutine
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
func (e *Engine) removeEmptyDestDirs() {
	var dirs []string
	_ = e.backend.WalkDir(e.destRoot, func(p string, d fs.DirEntry, err error) error {
		if err != nil || !d.IsDir() || p == e.destRoot || p == "." {
			return nil
		}
		// Same rule as the reconcile walk: this list is handed straight to
		// Remove, so anything that is not demonstrably inside the mirror root
		// does not go on it.
		if _, under := relTo(e.destRoot, p); !under {
			return nil
		}
		if d.Name() == VersionsDirName {
			return fs.SkipDir
		}
		dirs = append(dirs, p)
		return nil
	})
	for i := len(dirs) - 1; i >= 0; i-- { // deepest first
		_ = e.backend.Remove(dirs[i])
	}
}
