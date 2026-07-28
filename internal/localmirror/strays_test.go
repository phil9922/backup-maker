// SPDX-License-Identifier: MIT

package localmirror

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// strayWalker is a Backend that walks OUTSIDE the root it was asked to walk —
// the one thing the real backends never do, and the thing the reconciler must
// survive if one ever starts. It hands the reconciler a sibling directory whose
// name merely begins with the mirror root's, which is the exact shape that once
// made EnforceReceiveOnly force one of the user's own folders receive-only.
type strayWalker struct {
	Backend
	stray    string   // the path to smuggle into every walk
	diskRoot string   // where the fake backend's root really is, to stat the stray
	removed  []string // what the engine asked to delete
}

func (s *strayWalker) WalkDir(root string, fn fs.WalkDirFunc) error {
	if err := s.Backend.WalkDir(root, fn); err != nil {
		return err
	}
	// The stray arrives last, exactly as a buggy backend's extra entry would.
	info, err := os.Stat(filepath.FromSlash(s.strayOnDisk()))
	if err != nil {
		return err
	}
	return fn(s.stray, fs.FileInfoToDirEntry(info), nil)
}

func (s *strayWalker) Remove(p string) error {
	s.removed = append(s.removed, p)
	return s.Backend.Remove(p)
}

// strayOnDisk is only used to build a real fs.DirEntry for the fake walk entry.
func (s *strayWalker) strayOnDisk() string { return s.diskRoot + "/" + s.stray }

// THE RULE: nothing outside the mirror root may be versioned away or deleted,
// no matter what a backend claims to have found. The engine derives a relative
// path from every walk entry, and that relative path is what decides whether
// something is the version store, the marker, or ordinary user data — so a path
// it cannot honestly place must be skipped, not guessed at.
//
// The sibling here ("code-old" beside a mirror root ending "code") is the case
// a plain prefix test gets wrong, and getting it wrong means versioning away
// files the mirror does not own.
func TestAPathOutsideTheMirrorRootIsNeverTouched(t *testing.T) {
	src := t.TempDir()
	dstRoot := t.TempDir()
	if err := os.WriteFile(filepath.Join(src, "keep.txt"), []byte("real work"), 0o644); err != nil {
		t.Fatal(err)
	}

	base := NewLocalFS(dstRoot)
	e := newTestEngine(t, src, base, 0)

	// One good pass, so the mirror exists and the engine is in steady state.
	if _, _, err := e.reconcile(); err != nil {
		t.Fatalf("first pass: %v", err)
	}

	// A sibling of the mirror root, sharing its prefix, holding a file that
	// has nothing to do with this backup.
	mirror := e.destRoot // "<machine>/<label>"
	sibling := mirror + "-old"
	if err := os.MkdirAll(filepath.Join(dstRoot, filepath.FromSlash(sibling)), 0o755); err != nil {
		t.Fatal(err)
	}
	victim := filepath.Join(dstRoot, filepath.FromSlash(sibling), "not-ours.txt")
	if err := os.WriteFile(victim, []byte("somebody else's file"), 0o644); err != nil {
		t.Fatal(err)
	}

	stray := &strayWalker{Backend: base, stray: sibling + "/not-ours.txt", diskRoot: dstRoot}
	e.backend = stray

	if _, removed, err := e.reconcile(); err != nil {
		t.Fatalf("pass with a stray entry: %v", err)
	} else if removed != 0 {
		t.Errorf("the pass versioned away %d file(s) it does not own", removed)
	}

	// The assertion that matters is about the file on disk, not a counter.
	data, err := os.ReadFile(victim)
	if err != nil {
		t.Fatalf("a file outside the mirror root was destroyed: %v", err)
	}
	if string(data) != "somebody else's file" {
		t.Errorf("a file outside the mirror root was altered: %q", data)
	}
	for _, r := range stray.removed {
		if strings.HasPrefix(r, sibling) {
			t.Errorf("the engine asked to delete %q, which is outside its mirror root", r)
		}
	}
	// And no version of it was created either — versioning it away is the
	// same loss, just slower.
	versioned := filepath.Join(dstRoot, VersionsDirName, filepath.FromSlash(sibling))
	if _, err := os.Stat(versioned); err == nil {
		t.Errorf("a file outside the mirror root was copied into the version store at %s", versioned)
	}
}

// The boundary itself, stated directly: a sibling is not a child.
func TestRelToStopsAtASeparator(t *testing.T) {
	const root = "workstation/code"
	cases := []struct {
		p     string
		rel   string
		under bool
	}{
		{"workstation/code", "", true},
		{"workstation/code/main.go", "main.go", true},
		{"workstation/code/sub/deep.go", "sub/deep.go", true},
		{"workstation/code-old/main.go", "", false},
		{"workstation/codex", "", false},
		{"workstation", "", false},
		{"elsewhere/code/main.go", "", false},
	}
	for _, c := range cases {
		rel, under := relTo(root, c.p)
		if under != c.under || rel != c.rel {
			t.Errorf("relTo(%q, %q) = %q, %v; want %q, %v", root, c.p, rel, under, c.rel, c.under)
		}
	}
}
