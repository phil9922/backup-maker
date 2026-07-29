// SPDX-License-Identifier: MIT

package archive

import (
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	zip "github.com/yeka/zip"

	"github.com/phil9922/backup-maker/internal/config"
	"github.com/phil9922/backup-maker/internal/localmirror"
)

const (
	sharePasswordMarker = "MY-NAS-PASSWORD-1234"
	archiveKeyMarker    = "MY-SNAPSHOT-ENCRYPTION-KEY"
)

// snapshotWithConfigDirInside builds a folder shaped like a home directory:
// backup-maker's own configuration inside it, a decoy directory that merely
// carries the name, and junk the opt-out is supposed to keep.
func snapshotWithConfigDirInside(t *testing.T) (*config.Config, config.Archive, localmirror.Backend, string) {
	t.Helper()
	src := t.TempDir()
	dst := t.TempDir()

	t.Setenv("XDG_CONFIG_HOME", filepath.Join(src, ".config")) // linux
	t.Setenv("HOME", src)                                      // macOS
	dir, err := config.Dir()
	if err != nil {
		t.Fatal(err)
	}
	write := func(path, content string) {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write(filepath.Join(dir, "state.json"), sharePasswordMarker+" "+archiveKeyMarker)
	write(filepath.Join(dir, "syncthing-home", "key.pem"), archiveKeyMarker)
	write(filepath.Join(src, "notes.txt"), "a normal file")
	write(filepath.Join(src, "Development", "backup-maker", "main.go"), "package main")
	write(filepath.Join(src, "node_modules", "left-pad", "index.js"), "module.exports = 1")

	cfg := config.New()
	cfg.General.MachineName = "mach"
	cfg.Folders = []config.Folder{{ID: "f1", Path: src, Label: "home"}}
	job := config.Archive{Name: "testjob", Every: "daily", Target: "t", Keep: 2}
	return cfg, job, localmirror.NewLocalFS(dst), dst
}

// readArchive decrypts every entry of the snapshot Run wrote.
func readArchive(t *testing.T, dst, relFile, password string) map[string]string {
	t.Helper()
	zr, err := zip.OpenReader(filepath.Join(dst, filepath.FromSlash(relFile)))
	if err != nil {
		t.Fatal(err)
	}
	defer zr.Close()
	out := map[string]string{}
	for _, zf := range zr.File {
		zf.SetPassword(password)
		rc, err := zf.Open()
		if err != nil {
			t.Fatalf("open %s: %v", zf.Name, err)
		}
		data, _ := io.ReadAll(rc)
		rc.Close()
		out[zf.Name] = string(data)
	}
	return out
}

// The snapshot is encrypted with a password that lives in state.json, so
// putting state.json inside the snapshot would hand over the key with the lock.
// no_default_ignores turns off the junk filter and nothing else.
func TestSnapshotNeverIncludesTheConfigDir(t *testing.T) {
	cfg, job, b, dst := snapshotWithConfigDirInside(t)
	job.NoDefaultIgnores = true

	res := Run(b, cfg, job, "hunter2", slog.New(slog.DiscardHandler), nil)
	if res.Err != "" {
		t.Fatalf("run failed: %s", res.Err)
	}
	entries := readArchive(t, dst, res.File, "hunter2")

	for name, content := range entries {
		for _, marker := range []string{sharePasswordMarker, archiveKeyMarker} {
			if strings.Contains(content, marker) {
				t.Errorf("the snapshot holds secret %q at %s", marker, name)
			}
		}
	}
	if _, ok := entries["home/notes.txt"]; !ok {
		t.Error("a normal file is missing from the snapshot")
	}
	// The decoy: a directory that only shares the name, which would be the
	// user's own source tree on a real machine.
	if _, ok := entries["home/Development/backup-maker/main.go"]; !ok {
		t.Error("a directory merely named backup-maker was silently left out of the snapshot")
	}
	// Proof the opt-out really was on for everything else.
	if _, ok := entries["home/node_modules/left-pad/index.js"]; !ok {
		t.Error("no_default_ignores did not take effect; the junk filter is still on")
	}
}
