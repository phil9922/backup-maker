// SPDX-License-Identifier: MIT

package localmirror

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestVersionRoundTrip(t *testing.T) {
	root := t.TempDir()
	b := NewLocalFS(root)
	if err := os.MkdirAll(filepath.Join(root, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "sub", "file.txt"), []byte("v1"), 0o644); err != nil {
		t.Fatal(err)
	}

	now := time.Date(2026, 7, 21, 15, 30, 0, 0, time.Local)
	if err := keepVersion(b, "sub/file.txt", now); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "sub", "file.txt")); !os.IsNotExist(err) {
		t.Fatal("original should have moved into the version store")
	}
	want := filepath.Join(root, VersionsDirName, "sub", "file~20260721-153000.txt")
	data, err := os.ReadFile(want)
	if err != nil {
		t.Fatalf("expected version at %s: %v", want, err)
	}
	if string(data) != "v1" {
		t.Fatalf("version content = %q", data)
	}
}

func TestPrune(t *testing.T) {
	root := t.TempDir()
	b := NewLocalFS(root)
	now := time.Date(2026, 7, 21, 12, 0, 0, 0, time.Local)
	mk := func(age time.Duration) string {
		rel := versionPath("doc.txt", now.Add(-age))
		abs := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(abs, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
		return abs
	}

	tooOld := mk(40 * 24 * time.Hour) // beyond 30d maxAge: deleted
	dayOld := mk(25 * time.Hour)      // kept (daily slot)
	recent := mk(10 * time.Minute)    // kept (newest in its slot)

	if err := Prune(b, 30*24*time.Hour, now); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(tooOld); !os.IsNotExist(err) {
		t.Error("40-day-old version should be pruned")
	}
	for _, p := range []string{dayOld, recent} {
		if _, err := os.Stat(p); err != nil {
			t.Errorf("expected %s to survive: %v", filepath.Base(p), err)
		}
	}
}
