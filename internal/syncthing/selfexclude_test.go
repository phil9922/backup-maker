// SPDX-License-Identifier: MIT

package syncthing

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/phil9922/backup-maker/internal/config"
)

// homeWithConfigDirInside points the configuration directory inside the folder
// about to be sent to a paired machine, and returns the folder and the ignore
// line that must keep it here.
func homeWithConfigDirInside(t *testing.T) (config.Folder, string) {
	t.Helper()
	src := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(src, ".config")) // linux
	t.Setenv("HOME", src)                                      // macOS
	dir, err := config.Dir()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(src, "Development", "backup-maker"), 0o755); err != nil {
		t.Fatal(err)
	}
	rel, err := filepath.Rel(src, dir)
	if err != nil {
		t.Fatal(err)
	}
	return config.Folder{ID: "f1", Path: src, Label: "home"}, "/" + filepath.ToSlash(rel)
}

func hasLine(lines []string, want string) bool {
	for _, l := range lines {
		if l == want {
			return true
		}
	}
	return false
}

// A folder sent to a paired machine must not carry our configuration
// directory. The line is anchored to the folder root and comes first, so a
// user's own un-ignore cannot reach it, and it carries no (?d): the engine is
// never told it may delete our configuration.
func TestIgnoreLinesAnchorTheConfigDir(t *testing.T) {
	f, want := homeWithConfigDirInside(t)
	cfg := config.New()

	lines := IgnoreLines(cfg, f)
	if len(lines) == 0 || lines[0] != want {
		t.Fatalf("IgnoreLines = %v, want %q first", lines, want)
	}
	for _, l := range lines {
		if strings.HasSuffix(l, config.DirName) && !strings.HasPrefix(l, "/") {
			t.Errorf("line %q matches by name; it would also stop syncing the user's own directories called %q", l, config.DirName)
		}
	}
}

// no_default_ignores drops the junk patterns. It is not consent to send the
// passwords that decrypt this machine's snapshots to another computer.
func TestIgnoreLinesKeepTheConfigDirWithoutDefaultIgnores(t *testing.T) {
	f, want := homeWithConfigDirInside(t)
	f.NoDefaultIgnores = true
	cfg := config.New()

	lines := IgnoreLines(cfg, f)
	if !hasLine(lines, want) {
		t.Fatalf("IgnoreLines = %v, want it to contain %q", lines, want)
	}
	for _, l := range lines {
		if strings.HasPrefix(l, "(?d)") {
			t.Errorf("no_default_ignores left junk pattern %q in place", l)
		}
	}
}

// A folder the configuration directory is nowhere near gets the patterns it
// always got, and nothing else.
func TestIgnoreLinesLeaveOtherFoldersAlone(t *testing.T) {
	src := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(t.TempDir(), ".config"))
	t.Setenv("HOME", t.TempDir())
	if err := os.MkdirAll(filepath.Join(src, "backup-maker"), 0o755); err != nil {
		t.Fatal(err)
	}

	cfg := config.New()
	f := config.Folder{ID: "f1", Path: src, Label: "code", ExtraIgnore: []string{"tmp"}}
	for _, l := range IgnoreLines(cfg, f) {
		if !strings.HasPrefix(l, "(?d)") {
			t.Errorf("unexpected line %q for a folder that does not hold our configuration", l)
		}
	}
}

// The leftover copies already sitting on paired machines: warn while the stored
// ignores still lack the exclusion, and stop the moment they carry it — which
// is what keeps it to one warning per destination rather than one per reconcile.
func TestConfigAlreadySentReadsTheStoredIgnores(t *testing.T) {
	f, want := homeWithConfigDirInside(t)
	cfg := config.New()
	self := selfExcludeLines(f)
	if !hasLine(self, want) {
		t.Fatalf("selfExcludeLines = %v, want it to contain %q", self, want)
	}

	cases := []struct {
		name   string
		stored []string
		want   bool
	}{
		{"ignores from a release that did not exclude it", []string{"(?d)node_modules"}, true},
		{"a folder with no ignores at all", nil, true},
		{"ignores this version wrote", IgnoreLines(cfg, f), false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := configAlreadySent(c.stored, self); got != c.want {
				t.Errorf("configAlreadySent = %v, want %v", got, c.want)
			}
		})
	}

	// A folder that never held our configuration is never reported.
	if configAlreadySent(nil, nil) {
		t.Error("reported a leak for a folder the configuration directory is not in")
	}
}
