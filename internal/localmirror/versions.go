// SPDX-License-Identifier: MIT

package localmirror

import (
	"errors"
	"github.com/phil9922/backup-maker/internal/config"
	"io/fs"
	"path"
	"regexp"
	"sort"
	"strings"
	"time"
)

// VersionsDirName holds displaced file versions inside the target root,
// mirroring syncthing's .stversions concept and naming scheme
// (name~20060102-150405.ext).
//
// Defined in config and re-exported here: the destination's layout is shared
// with the code that deletes a stopped folder's backups, and one constant with
// two definitions is how a delete misses half of what it was asked to remove.
const VersionsDirName = config.VersionsDirName

const stampLayout = "20060102-150405"

// versionPath computes where a displaced target file goes in the store.
// relPath is slash-separated relative to the target root.
func versionPath(relPath string, now time.Time) string {
	dir, name := path.Split(relPath)
	ext := path.Ext(name)
	base := strings.TrimSuffix(name, ext)
	vname := base + "~" + now.Format(stampLayout) + ext
	return path.Join(VersionsDirName, dir, vname)
}

// keepVersion moves an existing target file into the version store instead of
// destroying it. Same-volume rename, so it is cheap and atomic.
func keepVersion(b Backend, relPath string, now time.Time) error {
	dst := versionPath(relPath, now)
	if err := b.MkdirAll(path.Dir(dst)); err != nil {
		return err
	}
	err := b.Rename(relPath, dst)
	if isNotExist(err) {
		return nil // nothing to preserve
	}
	return err
}

func isNotExist(err error) bool {
	return err != nil && errors.Is(err, fs.ErrNotExist)
}

var stampRe = regexp.MustCompile(`~(\d{8}-\d{6})`)

// Prune applies staggered retention inside the version store: keep one
// version per 30s for the first hour, per hour for the first day, per day up
// to maxAge; drop everything older.
func Prune(b Backend, maxAge time.Duration, now time.Time) error {
	if _, err := b.Stat(VersionsDirName); isNotExist(err) {
		return nil
	}

	type ver struct {
		path string
		ts   time.Time
	}
	groups := map[string][]ver{} // original relpath -> versions
	err := b.WalkDir(VersionsDirName, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		m := stampRe.FindStringSubmatch(d.Name())
		if m == nil {
			return nil
		}
		ts, perr := time.ParseInLocation(stampLayout, m[1], time.Local)
		if perr != nil {
			return nil
		}
		orig := stampRe.ReplaceAllString(p, "")
		groups[orig] = append(groups[orig], ver{path: p, ts: ts})
		return nil
	})
	if err != nil {
		return err
	}

	var firstErr error
	for orig, vs := range groups {
		sort.Slice(vs, func(i, j int) bool { return vs[i].ts.After(vs[j].ts) }) // newest first
		// DECIDED IN FULL BEFORE ANYTHING IS DELETED, so the rule below can ask
		// whether this group is about to disappear entirely.
		keeping := make([]bool, len(vs))
		var lastKept time.Time
		kept := 0
		for i, v := range vs {
			age := now.Sub(v.ts)
			var slot time.Duration
			switch {
			case age > maxAge:
				slot = -1 // too old: delete
			case age < time.Hour:
				slot = 30 * time.Second
			case age < 24*time.Hour:
				slot = time.Hour
			default:
				slot = 24 * time.Hour
			}
			if slot > 0 && (i == 0 || lastKept.Sub(v.ts) >= slot) {
				keeping[i] = true
				lastKept = v.ts
				kept++
			}
		}
		// THE LAST COPY IS NEVER TAKEN BY AGE ALONE.
		//
		// A file deleted from the source is not deleted on the destination — it is
		// moved into this store (see keepVersion, called from scan.go). From that
		// moment the version IS the file: there is no copy in the source and none
		// in the live mirror. Retention then deleted it like any other old version
		// once it passed maxAge, so a file removed from a source folder a month ago
		// vanished from every destination too, silently and for ever. That is the
		// one deletion this program must never perform on its own account: the
		// whole product exists so that losing a machine does not lose the files.
		//
		// So when a group is about to go entirely, keep its newest. A person can
		// still remove it by hand — retention just will not do it for them.
		//
		// COSTS ONE Stat, AND ONLY FOR A GROUP THAT WOULD OTHERWISE VANISH. Every
		// version being old is the only case where the last copy is at risk, and
		// over SMB a stat per version rather than per doomed path would be a round
		// trip for each of them.
		if kept == 0 && len(vs) > 0 && !liveCopyExists(b, orig) {
			keeping[0] = true // newest
		}
		for i, v := range vs {
			if keeping[i] {
				continue
			}
			if err := b.Remove(v.path); err != nil && firstErr == nil {
				firstErr = err
			}
		}
	}
	removeEmptyDirs(b, VersionsDirName)
	return firstErr
}

// liveCopyExists reports whether the file a version was made from is still in
// the live mirror on this destination.
//
// The version store is a parallel tree: .backup-maker-versions/<machine>/<label>/…
// mirrors <machine>/<label>/…, so the live path is this one with the store's
// prefix taken off. `orig` is a version path with its ~timestamp already
// stripped, which is exactly the live path once un-prefixed.
//
// FAILS TOWARDS KEEPING. A path that is not under the store, or a Stat that
// errors for any reason other than "not there", answers "a copy may exist" — the
// wrong answer costs a version nobody needed, and the other wrong answer costs
// somebody their only copy of a file.
func liveCopyExists(b Backend, orig string) bool {
	live, ok := LiveCopyOf(orig)
	if !ok {
		return true
	}
	_, err := b.Stat(live)
	return !isNotExist(err)
}

// LiveCopyOf is the live mirror path a version in the store was made from, and
// whether p is a path inside the store at all.
//
// EXPORTED SO THE STORE'S SHAPE HAS ONE READER. Both halves of it — the
// VersionsDirName prefix and the name~20060102-150405.ext stamp — are written
// by this file and nowhere else, and the file view asks the same question
// retention asks ("is this the last copy of that file?") in order to decide
// whether a person may delete it. A second parser of the naming scheme is how
// the two would come to different answers about the same file.
func LiveCopyOf(p string) (string, bool) {
	live, ok := strings.CutPrefix(p, VersionsDirName+"/")
	if !ok || live == "" {
		return "", false
	}
	live = stampRe.ReplaceAllString(live, "")
	if live == "" {
		return "", false
	}
	return live, true
}

// removeEmptyDirs tidies a subtree after pruning (best effort).
func removeEmptyDirs(b Backend, root string) {
	var dirs []string
	_ = b.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err == nil && d.IsDir() && p != root && p != "." {
			dirs = append(dirs, p)
		}
		return nil
	})
	// Deepest first so parents empty out.
	sort.Slice(dirs, func(i, j int) bool { return len(dirs[i]) > len(dirs[j]) })
	for _, d := range dirs {
		_ = b.Remove(d) // fails harmlessly if non-empty
	}
}
