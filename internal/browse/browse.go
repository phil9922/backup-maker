// SPDX-License-Identifier: MIT

// Package browse backs the dashboard's folder picker and drive detector.
//
// It is deliberately narrow: it lists DIRECTORIES ONLY and never returns file
// names, never reads file contents, and never accepts a relative path. The
// dashboard it serves is bound to 127.0.0.1 and requires the machine-owned
// token in state.json (mode 0600) — so any caller already runs as this user and
// could read the filesystem directly. This package exists to make picking a
// folder pleasant, not to expose anything new.
package browse

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// MaxEntries bounds a single listing so a directory with a pathological number
// of children can't stall the UI or balloon a JSON response.
const MaxEntries = 500

// Entry is one selectable directory.
type Entry struct {
	Name string `json:"name"`
	Path string `json:"path"`
}

// Drive is storage attached to this computer that could hold backups.
type Drive struct {
	Path  string `json:"path"`
	Label string `json:"label"`
	// Free and Total are bytes, or 0 when the platform couldn't report them.
	Free  uint64 `json:"free,omitempty"`
	Total uint64 `json:"total,omitempty"`
}

// Listing is one level of the picker.
type Listing struct {
	Path      string  `json:"path"`
	Parent    string  `json:"parent,omitempty"`
	Entries   []Entry `json:"entries"`
	Truncated bool    `json:"truncated,omitempty"`
}

// Dirs lists the sub-directories of an absolute path.
//
// Unreadable children are skipped rather than failing the whole listing: a
// single permission-denied subdirectory must not make the picker useless.
func Dirs(path string) (Listing, error) {
	if !filepath.IsAbs(path) {
		return Listing{}, fmt.Errorf("path must be absolute: %q", path)
	}
	clean := filepath.Clean(path)
	fi, err := os.Stat(clean)
	if err != nil {
		return Listing{}, err
	}
	if !fi.IsDir() {
		return Listing{}, fmt.Errorf("not a directory: %s", clean)
	}

	f, err := os.Open(clean)
	if err != nil {
		return Listing{}, err
	}
	defer f.Close()
	names, err := f.Readdirnames(-1)
	if err != nil {
		return Listing{}, err
	}
	sort.Slice(names, func(i, j int) bool {
		return strings.ToLower(names[i]) < strings.ToLower(names[j])
	})

	out := Listing{Path: clean}
	if parent := filepath.Dir(clean); parent != clean {
		out.Parent = parent
	}
	for _, name := range names {
		if len(out.Entries) >= MaxEntries {
			out.Truncated = true
			break
		}
		full := filepath.Join(clean, name)
		// Stat (not Lstat) so a symlinked directory still reads as a directory,
		// then confirm it hasn't escaped the directory being listed.
		st, err := os.Stat(full)
		if err != nil || !st.IsDir() {
			continue
		}
		if escapes(clean, full) {
			continue
		}
		out.Entries = append(out.Entries, Entry{Name: name, Path: full})
	}
	return out, nil
}

// escapes reports whether child resolves to somewhere outside parent, which
// only happens via a symlink pointing away.
func escapes(parent, child string) bool {
	real, err := filepath.EvalSymlinks(child)
	if err != nil {
		return true // can't prove it's safe; leave it out
	}
	realParent, err := filepath.EvalSymlinks(parent)
	if err != nil {
		realParent = parent
	}
	rel, err := filepath.Rel(realParent, real)
	if err != nil {
		return true
	}
	return rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// Roots are the sensible starting points for the picker: the home directory
// first, then the well-known folders inside it that actually exist.
func Roots() []Entry {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil
	}
	roots := []Entry{{Name: "Home", Path: home}}
	for _, name := range []string{"Documents", "Desktop", "Pictures", "Music", "Videos"} {
		p := filepath.Join(home, name)
		if fi, err := os.Stat(p); err == nil && fi.IsDir() {
			roots = append(roots, Entry{Name: name, Path: p})
		}
	}
	return roots
}
