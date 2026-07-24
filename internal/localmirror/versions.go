// SPDX-License-Identifier: MIT

package localmirror

import (
	"errors"
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
const VersionsDirName = ".backup-maker-versions"

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
	for _, vs := range groups {
		sort.Slice(vs, func(i, j int) bool { return vs[i].ts.After(vs[j].ts) }) // newest first
		var lastKept time.Time
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
			keep := slot > 0 && (i == 0 || lastKept.Sub(v.ts) >= slot)
			if keep {
				lastKept = v.ts
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
