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

// Reasons a piece of storage cannot hold backups yet. The dashboard picks its
// own wording from these rather than parsing Detail.
const (
	ReasonBlank     = "blank"       // attached, nothing partitioned on it
	ReasonUnmounted = "unmounted"   // has partitions, none of them mounted
	ReasonReadOnly  = "readonly"    // the kernel marks the whole disk read-only
	ReasonNotAMount = "not-a-mount" // a directory that only looks like a drive
)

// Unusable is storage that is present but cannot hold backups yet, with the
// reason in words.
//
// It exists because the worst thing this program can say to somebody holding a
// drive they have just plugged in is that no drives are plugged in. Drives()
// lists mounted directories, so a disk with no filesystem is invisible to it —
// this type carries what we could see and why it is not usable.
type Unusable struct {
	// Device is the kernel path (/dev/sda) for a whole disk, empty for a
	// directory that is not a mount point.
	Device string `json:"device,omitempty"`
	// Name is what to call it on screen: an external drive's own product name
	// where the bus reports one, otherwise the disk's vendor and model.
	Name string `json:"name"`
	Size uint64 `json:"size,omitempty"`
	// Bus is usb, nvme or mmc where known — a USB disk often reports
	// removable=0, so the bus is what actually tells an external drive apart.
	Bus    string `json:"bus,omitempty"`
	Reason string `json:"reason"`
	Detail string `json:"detail"`
	// Path is the directory concerned, for ReasonNotAMount.
	Path string `json:"path,omitempty"`
	// Free is bytes free, reported only where there is a filesystem to ask.
	Free uint64 `json:"free,omitempty"`
	// Confirm is what the user must type back before this drive is prepared.
	// Computed here so the dashboard and the privileged helper that re-checks
	// it can never disagree about the expected string.
	Confirm string `json:"confirm,omitempty"`
}

// HumanSize renders a capacity the way the dashboard shows it.
//
// It is here, rather than in either caller, because it is part of the string a
// user types back to confirm formatting a drive: the page that displays it and
// the privileged helper that checks it must round identically or the
// confirmation can never be satisfied.
func HumanSize(n uint64) string {
	switch {
	case n >= 1<<40:
		return fmt.Sprintf("%.1fTB", float64(n)/(1<<40))
	case n >= 1<<30:
		return fmt.Sprintf("%.1fGB", float64(n)/(1<<30))
	case n >= 1<<20:
		return fmt.Sprintf("%.1fMB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.1fKB", float64(n)/(1<<10))
	default:
		return fmt.Sprintf("%dB", n)
	}
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
	// HIDDEN FOLDERS SORT LAST, AND ARE STILL LISTED.
	//
	// Alphabetically a dot sorts before every letter, so opening a home
	// directory answered with .cache, .config, .cursor and a dozen more before
	// Desktop or Documents — about twenty rows of things nobody came here for,
	// in front of the two everybody does. That only became the first thing the
	// picker shows when Roots stopped listing Documents and Desktop beside Home.
	//
	// SORTED, NOT HIDDEN. This is a backup tool: ~/.ssh, ~/.config and ~/.gnupg
	// are exactly the sort of thing somebody means to protect, and a picker that
	// leaves them out by default would let a person set up a backup they believe
	// is complete and is not. Ordering costs them nothing; concealment could
	// cost them the files.
	sort.Slice(names, func(i, j int) bool {
		hi, hj := strings.HasPrefix(names[i], "."), strings.HasPrefix(names[j], ".")
		if hi != hj {
			return hj
		}
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

// Roots are the starting points for the picker: the home directory, and nothing
// that lives inside it.
//
// IT USED TO LIST Documents, Desktop, Pictures, Music AND Videos BESIDE Home,
// as shortcuts. They are not shortcuts once the picker can choose more than one
// folder — they are a parent and five of its children presented as a flat list
// of equals, and the step's question is which folders to protect. Choosing Home
// and Documents there means copying those files twice to every destination, for
// ever, and packing two copies of each into every snapshot. The wizard now
// refuses that pairing, but a list that invites it and is then refused is a
// worse answer than a list that never offered it: the folders inside home are
// reached by opening home, which is what a folder picker is for.
//
// NO ROOT MAY CONTAIN ANOTHER, which is the rule rather than the current list —
// so a mount point added here later cannot quietly recreate the same trap.
func Roots() []Entry {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil
	}
	return withoutNested([]Entry{{Name: "Home", Path: home}})
}

// withoutNested drops any entry that sits inside another one.
func withoutNested(in []Entry) []Entry {
	out := make([]Entry, 0, len(in))
	for i, e := range in {
		nested := false
		for j, other := range in {
			if i == j {
				continue
			}
			rel, err := filepath.Rel(other.Path, e.Path)
			if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
				continue
			}
			nested = true
			break
		}
		if !nested {
			out = append(out, e)
		}
	}
	return out
}
