// SPDX-License-Identifier: MIT

package localmirror

import (
	"bytes"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/phil9922/backup-maker/internal/config"
)

// The markers stand in for the real thing: the share password and the archive
// password that decrypts the snapshots, both of which live in state.json.
const (
	sharePasswordMarker = "MY-NAS-PASSWORD-1234"
	archiveKeyMarker    = "MY-SNAPSHOT-ENCRYPTION-KEY"
)

// plantConfigDir reproduces the leak: backup-maker's own configuration
// directory, holding this machine's secrets, inside the folder being backed up
// — which is what happens the moment someone picks "Home" in the wizard.
func plantConfigDir(t *testing.T, src string) string {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(src, ".config")) // linux
	t.Setenv("HOME", src)                                      // macOS
	dir, err := config.Dir()
	if err != nil {
		t.Fatalf("config.Dir: %v", err)
	}
	state := `{"share_credentials":{"nas":"` + sharePasswordMarker + `"},` +
		`"archive_passwords":{"nightly":"` + archiveKeyMarker + `"}}`
	if err := os.WriteFile(filepath.Join(dir, "state.json"), []byte(state), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "syncthing-home"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "syncthing-home", "key.pem"), []byte(archiveKeyMarker), 0o600); err != nil {
		t.Fatal(err)
	}
	return dir
}

// assertNoSecretsUnder is the assertion that matters: not "the path is absent"
// but "the secret is nowhere on this destination".
func assertNoSecretsUnder(t *testing.T, root string) {
	t.Helper()
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		data, rerr := os.ReadFile(p)
		if rerr != nil {
			return rerr
		}
		for _, marker := range []string{sharePasswordMarker, archiveKeyMarker} {
			if bytes.Contains(data, []byte(marker)) {
				rel, _ := filepath.Rel(root, p)
				t.Errorf("the destination holds secret %q at %s", marker, rel)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walking the destination: %v", err)
	}
}

func mustWrite(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// engineWithIgnores builds an engine the way the daemon does, with whatever
// pattern list the folder's settings produced.
func engineWithIgnores(t *testing.T, src string, b Backend, ignores []string, log *slog.Logger) *Engine {
	t.Helper()
	return New(Options{
		FolderID:    "f1",
		TargetName:  "dest",
		SourcePath:  src,
		Backend:     b,
		MachineName: "workstation",
		Label:       "home",
		MaxAgeDays:  30,
		Ignores:     ignores,
		Log:         log,
	})
}

// The bug: backing up a home directory mirrored backup-maker's own state.json
// — share passwords, archive passwords, sync identity — to the destination.
func TestConfigDirIsNeverMirrored(t *testing.T) {
	src := t.TempDir()
	dstRoot := t.TempDir()
	plantConfigDir(t, src)
	mustWrite(t, filepath.Join(src, "notes.txt"), "a normal file")
	mustWrite(t, filepath.Join(src, ".config", "other-app", "settings.conf"), "someone else's config")

	e := engineWithIgnores(t, src, NewLocalFS(dstRoot), config.DefaultIgnores, quietLog())
	if _, _, err := e.reconcile(); err != nil {
		t.Fatalf("reconcile: %v", err)
	}

	assertNoSecretsUnder(t, dstRoot)
	mirror := filepath.Join(dstRoot, "workstation", "home")
	if !exists(filepath.Join(mirror, "notes.txt")) {
		t.Error("a normal file was not backed up")
	}
	// Only OUR directory goes; the rest of ~/.config is the user's own data.
	if !exists(filepath.Join(mirror, ".config", "other-app", "settings.conf")) {
		t.Error("another application's config was dropped; only backup-maker's own may be")
	}
}

// The regression that would be worse than the leak: a directory called
// backup-maker that is not ours (this project's source tree, for instance)
// must be backed up like anything else.
func TestADirectoryMerelyNamedBackupMakerIsMirrored(t *testing.T) {
	src := t.TempDir()
	dstRoot := t.TempDir()
	plantConfigDir(t, src)
	mustWrite(t, filepath.Join(src, "Development", "backup-maker", "main.go"), "package main")
	mustWrite(t, filepath.Join(src, "Development", "backup-maker", "internal", "config", "paths.go"), "package config")

	e := engineWithIgnores(t, src, NewLocalFS(dstRoot), config.DefaultIgnores, quietLog())
	if _, _, err := e.reconcile(); err != nil {
		t.Fatalf("reconcile: %v", err)
	}

	mirror := filepath.Join(dstRoot, "workstation", "home")
	for _, rel := range []string{
		filepath.Join("Development", "backup-maker", "main.go"),
		filepath.Join("Development", "backup-maker", "internal", "config", "paths.go"),
	} {
		if !exists(filepath.Join(mirror, rel)) {
			t.Errorf("%s was silently left out of the backup", rel)
		}
	}
	assertNoSecretsUnder(t, dstRoot)
}

// no_default_ignores exists so a folder can keep node_modules. It is not
// consent to copy the user's own passwords, so it must not reach this rule.
func TestNoDefaultIgnoresStillExcludesTheConfigDir(t *testing.T) {
	src := t.TempDir()
	dstRoot := t.TempDir()
	plantConfigDir(t, src)
	mustWrite(t, filepath.Join(src, "node_modules", "left-pad", "index.js"), "module.exports = 1")

	// The daemon passes no patterns at all when the folder opts out.
	e := engineWithIgnores(t, src, NewLocalFS(dstRoot), nil, quietLog())
	if _, _, err := e.reconcile(); err != nil {
		t.Fatalf("reconcile: %v", err)
	}

	mirror := filepath.Join(dstRoot, "workstation", "home")
	if !exists(filepath.Join(mirror, "node_modules", "left-pad", "index.js")) {
		t.Error("no_default_ignores did not take effect; the junk filter is still on")
	}
	assertNoSecretsUnder(t, dstRoot)
}

// A folder that does not contain the configuration directory is untouched by
// the rule.
func TestConfigDirOutsideTheFolderChangesNothing(t *testing.T) {
	src := t.TempDir()
	dstRoot := t.TempDir()
	plantConfigDir(t, t.TempDir()) // somewhere else entirely
	mustWrite(t, filepath.Join(src, ".config", "backup-maker-notes.txt"), "not ours")
	mustWrite(t, filepath.Join(src, "notes.txt"), "a normal file")

	e := engineWithIgnores(t, src, NewLocalFS(dstRoot), config.DefaultIgnores, quietLog())
	if _, _, err := e.reconcile(); err != nil {
		t.Fatalf("reconcile: %v", err)
	}

	mirror := filepath.Join(dstRoot, "workstation", "home")
	for _, rel := range []string{"notes.txt", filepath.Join(".config", "backup-maker-notes.txt")} {
		if !exists(filepath.Join(mirror, rel)) {
			t.Errorf("%s was not backed up", rel)
		}
	}
}

// Destinations already hold leaked copies from v0.1.1. They cannot be deleted
// from here — nothing is ever deleted from a mirror — so the user is told,
// once, rather than every hour.
func TestLeftoverConfigCopyIsReportedOnce(t *testing.T) {
	src := t.TempDir()
	dstRoot := t.TempDir()
	dir := plantConfigDir(t, src)
	mustWrite(t, filepath.Join(src, "notes.txt"), "a normal file")

	// What an earlier release left on the destination.
	leakedRel, err := filepath.Rel(src, dir)
	if err != nil {
		t.Fatal(err)
	}
	leaked := filepath.Join(dstRoot, "workstation", "home", leakedRel, "state.json")
	mustWrite(t, leaked, sharePasswordMarker)

	var logs bytes.Buffer
	e := engineWithIgnores(t, src, NewLocalFS(dstRoot), config.DefaultIgnores,
		slog.New(slog.NewTextHandler(&logs, nil)))

	if _, _, err := e.reconcile(); err != nil {
		t.Fatalf("first reconcile: %v", err)
	}
	// Put one back, so the second pass would have every reason to warn again:
	// only the recorded state stops it.
	mustWrite(t, leaked, sharePasswordMarker)
	if _, _, err := e.reconcile(); err != nil {
		t.Fatalf("second reconcile: %v", err)
	}

	const phrase = "holds a copy of backup-maker"
	if n := strings.Count(logs.String(), phrase); n != 1 {
		t.Errorf("two scans produced %d warnings, want exactly 1\n%s", n, logs.String())
	}

	// The leaked copy is displaced into the version store, never destroyed:
	// deleting from a destination is not this program's decision to make.
	var kept bool
	_ = filepath.WalkDir(filepath.Join(dstRoot, VersionsDirName), func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		if data, rerr := os.ReadFile(p); rerr == nil && bytes.Contains(data, []byte(sharePasswordMarker)) {
			kept = true
		}
		return nil
	})
	if !kept {
		t.Error("the leaked copy was deleted from the destination; a mirror must only ever displace files into the version store")
	}
}

// The primitive underneath: anchored, so the path decides, not the name.
func TestMatcherForAnchorsTheConfigDir(t *testing.T) {
	src := t.TempDir()
	dir := plantConfigDir(t, src)
	rel, err := filepath.Rel(src, dir)
	if err != nil {
		t.Fatal(err)
	}
	rel = filepath.ToSlash(rel)

	m := NewMatcherFor(src, nil)
	cases := []struct {
		path string
		want bool
	}{
		{rel, true},
		{rel + "/state.json", true},
		{rel + "/syncthing-home/key.pem", true},
		{"Development/backup-maker/main.go", false},
		{"backup-maker/README.md", false},
		{".config/other-app/settings.conf", false},
		{"notes.txt", false},
	}
	for _, c := range cases {
		if got := m.Ignored(c.path); got != c.want {
			t.Errorf("Ignored(%q) = %v, want %v", c.path, got, c.want)
		}
	}
}
