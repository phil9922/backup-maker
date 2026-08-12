// SPDX-License-Identifier: MIT

package config

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// PathWithin decides whether a file written at one path would land inside a
// folder that is being backed up — the question the snapshot spool asks before
// it writes tens of gigabytes anywhere.
//
// DIRECTIONAL, unlike the containment relInside answers for the self-exclusion:
// a directory that merely CONTAINS a backed-up folder holds nothing that gets
// copied, and refusing it would rule out a home directory with one backed-up
// folder under it for no reason at all.
func TestPathWithinIsDirectional(t *testing.T) {
	home := t.TempDir()
	src := filepath.Join(home, "code")
	if err := os.MkdirAll(filepath.Join(src, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		name       string
		base, targ string
		want       bool
	}{
		{"the folder itself", src, src, true},
		{"a directory inside it", src, filepath.Join(src, "sub"), true},
		{"one that does not exist yet", src, filepath.Join(src, "sub", "spool"), true},
		{"a sibling", src, filepath.Join(home, "codespool"), false},
		{"the parent", src, home, false},
		{"somewhere else entirely", src, t.TempDir(), false},
	} {
		if got := PathWithin(tc.base, tc.targ); got != tc.want {
			t.Errorf("%s: PathWithin(%q, %q) = %v, want %v", tc.name, tc.base, tc.targ, got, tc.want)
		}
	}

	// Reached by another route. /tmp is a symlink on some systems and a home
	// directory can be spelled more than one way, so a single comparison would
	// miss the match that matters most.
	if runtime.GOOS == "windows" {
		return
	}
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(src, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if !PathWithin(src, filepath.Join(link, "sub")) {
		t.Errorf("a path reaching %s through a symlink was not recognised as inside it", src)
	}
}
