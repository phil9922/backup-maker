// SPDX-License-Identifier: MIT

package localmirror

import (
	"io"
	"io/fs"
	"sort"
	"strings"
	"testing"
	"time"
)

// ExerciseBackend is a conformance suite run against every Backend
// implementation (localFS in this package's tests, smbfs in its own
// integration tests). It uses only the Backend interface.
func ExerciseBackend(t testing.TB, b Backend) {
	t.Helper()

	// WriteFile / ReadFile round-trip.
	if err := b.WriteFile("marker.json", []byte(`{"ok":true}`)); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	data, err := b.ReadFile("marker.json")
	if err != nil || string(data) != `{"ok":true}` {
		t.Fatalf("ReadFile = %q, %v", data, err)
	}

	// MkdirAll + OpenWrite + Sync + Rename (atomic write dance).
	if err := b.MkdirAll("a/b"); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	w, err := b.OpenWrite("a/b/.file.txt.tmp")
	if err != nil {
		t.Fatalf("OpenWrite: %v", err)
	}
	if _, err := io.WriteString(w, "content-v1"); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if err := w.Sync(); err != nil {
		t.Fatalf("Sync: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	mtime := time.Now().Add(-time.Hour).Truncate(time.Second)
	if err := b.Chtimes("a/b/.file.txt.tmp", time.Now(), mtime); err != nil {
		t.Fatalf("Chtimes: %v", err)
	}
	if err := b.Rename("a/b/.file.txt.tmp", "a/b/file.txt"); err != nil {
		t.Fatalf("Rename: %v", err)
	}

	// Stat honors size and (tolerantly) mtime.
	fi, err := b.Stat("a/b/file.txt")
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if fi.Size() != int64(len("content-v1")) {
		t.Errorf("Stat size = %d", fi.Size())
	}
	delta := fi.ModTime().Sub(mtime)
	if delta < 0 {
		delta = -delta
	}
	if delta > mtimeTolerance {
		// Informational: some servers (impacket, odd NAS firmware) ignore
		// SetInfo timestamps. The engine detects this and self-calibrates.
		t.Logf("note: Chtimes not honored by this backend (got %v want %v) — engine will use size+recency comparison", fi.ModTime(), mtime)
	}

	// Rename over an existing file must replace it.
	w2, err := b.OpenWrite("a/b/.file.txt.tmp")
	if err != nil {
		t.Fatalf("OpenWrite2: %v", err)
	}
	io.WriteString(w2, "content-v2")
	w2.Close()
	if err := b.Rename("a/b/.file.txt.tmp", "a/b/file.txt"); err != nil {
		t.Fatalf("Rename over existing: %v", err)
	}
	if data, _ := b.ReadFile("a/b/file.txt"); string(data) != "content-v2" {
		t.Errorf("after replace, content = %q", data)
	}

	// OpenRead.
	r, err := b.OpenRead("a/b/file.txt")
	if err != nil {
		t.Fatalf("OpenRead: %v", err)
	}
	got, _ := io.ReadAll(r)
	r.Close()
	if string(got) != "content-v2" {
		t.Errorf("OpenRead = %q", got)
	}

	// WalkDir yields root-relative slash paths.
	var seen []string
	err = b.WalkDir("a", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			seen = append(seen, p)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("WalkDir: %v", err)
	}
	sort.Strings(seen)
	if len(seen) != 1 || seen[0] != "a/b/file.txt" {
		t.Errorf("WalkDir files = %v, want [a/b/file.txt]", seen)
	}

	// Stat of a missing path must satisfy errors.Is(err, fs.ErrNotExist).
	if _, err := b.Stat("a/nope.txt"); !isNotExist(err) {
		t.Errorf("Stat(missing) = %v, want fs.ErrNotExist", err)
	}

	// Remove / RemoveAll.
	if err := b.Remove("a/b/file.txt"); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if err := b.RemoveAll("a"); err != nil {
		t.Fatalf("RemoveAll: %v", err)
	}
	if _, err := b.Stat("a"); !isNotExist(err) {
		t.Errorf("RemoveAll left 'a' behind (err=%v)", err)
	}
	if err := b.Remove("marker.json"); err != nil {
		t.Fatalf("Remove marker: %v", err)
	}

	// Paths never leak OS separators back to callers.
	if strings.Contains(strings.Join(seen, ""), "\\") {
		t.Error("WalkDir returned backslash paths")
	}
}
