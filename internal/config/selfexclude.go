// SPDX-License-Identifier: MIT

package config

import (
	"path/filepath"
	"strings"
)

// SelfExcludes reports where backup-maker's own configuration directory sits
// inside the folder rooted at root, as slash-separated paths relative to root.
// It returns nothing when that directory lies somewhere else entirely, which is
// the ordinary case — the exclusion must do nothing at all to a folder that
// does not contain it.
//
// This is a SAFETY rule, not a junk filter, which is why it lives here and not
// in DefaultIgnores: the directory holds state.json (share passwords, the
// archive passwords that decrypt the snapshots, the dashboard token), the sync
// engine's certificate and private key, and the logs. Copying it to a
// destination hands whoever can read that destination the keys to everything
// stored on it — for a share, the password to the share itself. No setting
// reaches this, and no_default_ignores in particular does not: that flag exists
// so a snapshot can keep node_modules, not to consent to publishing passwords.
//
// The result is PATH-ANCHORED, never a name pattern. A bare "backup-maker"
// ignore would also drop any directory that merely happens to be NAMED that —
// this project's own source tree, for one — and silently leaving a user's code
// out of their backups would be a worse bug than the leak this prevents. Only a
// directory that genuinely resolves to the configuration directory matches.
//
// A single empty string means root IS the configuration directory (or sits
// inside it), so nothing under it may be copied at all.
func SelfExcludes(root string) []string {
	self, err := dirPath()
	if err != nil {
		return nil // no home to build a config dir from: nothing to protect
	}
	// Both paths are compared resolved: os.UserConfigDir builds on $HOME, and
	// the folder the user picked may reach the same place by another route (a
	// symlinked /home, or macOS's /var -> /private/var). The unresolved pair is
	// checked too, so a path that cannot be resolved right now — either side may
	// not exist yet — is still protected by its plain absolute form.
	var out []string
	seen := map[string]bool{}
	for _, pair := range [][2]string{
		{resolvePath(root), resolvePath(self)},
		{absPath(root), absPath(self)},
	} {
		rel, ok := relInside(pair[0], pair[1])
		if !ok || seen[rel] {
			continue
		}
		seen[rel] = true
		out = append(out, rel)
	}
	return out
}

// relInside returns target's location relative to base, slash-separated, when
// target is base or lies inside it. A base that sits inside target reports the
// empty string: the whole of base is then part of the configuration directory.
func relInside(base, target string) (string, bool) {
	rel, err := filepath.Rel(base, target)
	if err != nil {
		return "", false // unrelated volumes on Windows
	}
	if rel == "." {
		return "", true
	}
	if outside(rel) {
		if back, berr := filepath.Rel(target, base); berr == nil && !outside(back) {
			return "", true // backing up a directory inside the config dir
		}
		return "", false
	}
	return filepath.ToSlash(rel), true
}

// outside reports whether a result of filepath.Rel leaves the base directory.
// The separator boundary matters: a child literally named "..keep" is inside.
func outside(rel string) bool {
	return rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// resolvePath is p absolute and free of symlinks, falling back to the plain
// absolute path when it cannot be resolved (it may not exist yet).
func resolvePath(p string) string {
	abs := absPath(p)
	resolved, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return abs
	}
	return resolved
}

func absPath(p string) string {
	abs, err := filepath.Abs(p)
	if err != nil {
		return filepath.Clean(p)
	}
	return abs
}
