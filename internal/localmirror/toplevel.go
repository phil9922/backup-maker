// SPDX-License-Identifier: MIT

package localmirror

import (
	"io/fs"
	"sort"
)

// TopLevelDirs lists the immediate subdirectories of a destination root — the
// machine directories, in practice, since that is the only thing that sits at
// that level.
//
// WHY IT IS NOT A ReadDir ON Backend. The interface deliberately has no
// directory listing: everything the mirror does walks a tree it already knows
// the shape of. Adding ReadDir would mean implementing and testing it on both
// backends for one caller. WalkDir with SkipDir gets the same answer through
// the door that is already open, and the SMB backend's WalkDir is the one that
// has been exercised against real Samba.
//
// The two backends disagree about what they call the root — filepath.Rel gives
// "." while the SMB walk gives "" — so both are treated as the root here rather
// than in each caller.
func TopLevelDirs(b Backend) ([]string, error) {
	var out []string
	err := b.WalkDir("", func(p string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			// An unreadable root is the caller's problem, reported by the
			// returned error. An unreadable entry inside it is not worth
			// abandoning the rest of the listing for.
			if p == "" || p == "." {
				return walkErr
			}
			return nil
		}
		if p == "" || p == "." {
			return nil
		}
		if !d.IsDir() {
			return nil
		}
		out = append(out, p)
		// Exactly one level. Descending would turn a listing of a handful of
		// machine names into a walk of every backed-up file on the drive, on a
		// path that runs once a minute.
		return fs.SkipDir
	})
	if err != nil {
		return nil, err
	}
	sort.Strings(out)
	return out, nil
}
