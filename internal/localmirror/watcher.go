// SPDX-License-Identifier: MIT

package localmirror

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"github.com/fsnotify/fsnotify"
)

// watch streams debounced change notifications into e.kick until ctx ends.
// fsnotify is not recursive, so directories are watched individually and new
// ones are added as they appear. Watching is an optimization only — the
// periodic reconcile pass guarantees correctness if events are missed, and
// the engine degrades to scan-only mode if the watcher can't run (e.g.
// inotify watch limits).
func (e *Engine) watch(ctx context.Context) {
	w, err := fsnotify.NewWatcher()
	if err != nil {
		e.log.Warn("watcher unavailable; relying on periodic scans", "err", err)
		return
	}
	defer w.Close()

	addTree := func(root string) {
		_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if err != nil || !d.IsDir() {
				return nil
			}
			rel, rerr := filepath.Rel(e.sourcePath, path)
			if rerr == nil && rel != "." && e.ignore.Ignored(rel) {
				return filepath.SkipDir
			}
			if werr := w.Add(path); werr != nil {
				e.log.Warn("cannot watch directory; periodic scans still cover it", "dir", path, "err", werr)
			}
			return nil
		})
	}
	addTree(e.sourcePath)

	// Debounce: fire one kick 1s after the last event in a burst.
	var timer *time.Timer
	fire := func() {
		select {
		case e.kick <- struct{}{}:
		default: // a kick is already pending
		}
	}

	for {
		select {
		case <-ctx.Done():
			return
		case ev, ok := <-w.Events:
			if !ok {
				return
			}
			rel, err := filepath.Rel(e.sourcePath, ev.Name)
			if err != nil || e.ignore.Ignored(rel) {
				continue
			}
			if ev.Has(fsnotify.Create) {
				if fi, err := os.Lstat(ev.Name); err == nil && fi.IsDir() {
					addTree(ev.Name)
				}
			}
			if timer != nil {
				timer.Stop()
			}
			timer = time.AfterFunc(time.Second, fire)
		case err, ok := <-w.Errors:
			if !ok {
				return
			}
			e.log.Warn("watcher error; periodic scans still cover changes", "err", err)
		}
	}
}
