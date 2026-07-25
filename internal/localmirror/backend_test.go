// SPDX-License-Identifier: MIT

package localmirror

import (
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestLocalFSConformance(t *testing.T) {
	ExerciseBackend(t, NewLocalFS(t.TempDir()))
}

// TestEngineEndToEnd runs the reconcile cycle against a localFS target:
// initial copy, incremental update, delete-as-versioning, ignore handling.
func TestEngineEndToEnd(t *testing.T) {
	src := t.TempDir()
	dst := t.TempDir()

	write := func(rel, content string) {
		p := filepath.Join(src, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("main.go", "package main")
	write("docs/readme.md", "# hi")
	write("node_modules/x/y.js", "junk")

	b := NewLocalFS(dst)
	if err := WriteMarker(b, "uuid-1", "mach"); err != nil {
		t.Fatal(err)
	}
	e := New(Options{
		FolderID: "f1", TargetName: "t1", SourcePath: src, Backend: b,
		MachineName: "mach", Label: "proj", UUID: "uuid-1", MaxAgeDays: 30,
		Ignores: []string{"node_modules"}, Log: slog.New(slog.DiscardHandler),
	})

	e.sync()
	mirror := filepath.Join(dst, "mach", "proj")
	if data, err := os.ReadFile(filepath.Join(mirror, "main.go")); err != nil || string(data) != "package main" {
		t.Fatalf("main.go not mirrored: %q %v", data, err)
	}
	if _, err := os.Stat(filepath.Join(mirror, "node_modules")); !os.IsNotExist(err) {
		t.Error("ignored dir was mirrored")
	}

	// Incremental update.
	time.Sleep(10 * time.Millisecond)
	write("main.go", "package main // v2")
	// Force mtime difference beyond tolerance.
	past := time.Now().Add(10 * time.Second)
	os.Chtimes(filepath.Join(src, "main.go"), past, past)
	e.sync()
	if data, _ := os.ReadFile(filepath.Join(mirror, "main.go")); string(data) != "package main // v2" {
		t.Fatalf("update not mirrored: %q", data)
	}

	// Delete propagates as versioning.
	os.Remove(filepath.Join(src, "docs", "readme.md"))
	e.sync()
	if _, err := os.Stat(filepath.Join(mirror, "docs", "readme.md")); !os.IsNotExist(err) {
		t.Error("deleted file still in mirror")
	}
	matches, _ := filepath.Glob(filepath.Join(dst, VersionsDirName, "mach", "proj", "docs", "readme~*.md"))
	if len(matches) != 1 {
		t.Errorf("expected 1 version of readme.md, found %v", matches)
	}

	// Wrong marker: refuses to write.
	if err := WriteMarker(b, "other-uuid", "mach"); err != nil {
		t.Fatal(err)
	}
	write("late.txt", "should not appear")
	e.sync()
	if st := e.Status(); st.State != "wrong-drive" {
		t.Errorf("state = %q, want wrong-drive", st.State)
	}
	if _, err := os.Stat(filepath.Join(mirror, "late.txt")); !os.IsNotExist(err) {
		t.Error("engine wrote to a foreign target")
	}
}

func TestVerifyCopy(t *testing.T) {
	src := t.TempDir()
	dst := t.TempDir()
	srcFile := filepath.Join(src, "f.txt")
	if err := os.WriteFile(srcFile, []byte("verify me"), 0o644); err != nil {
		t.Fatal(err)
	}
	b := NewLocalFS(dst)
	if err := copyFile(b, srcFile, "f.txt", time.Now(), true); err != nil {
		t.Fatalf("verified copy failed: %v", err)
	}
	if data, _ := os.ReadFile(filepath.Join(dst, "f.txt")); string(data) != "verify me" {
		t.Errorf("content = %q", data)
	}
}
