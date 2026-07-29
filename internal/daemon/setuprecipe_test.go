// SPDX-License-Identifier: MIT

package daemon

import (
	"strings"
	"testing"
)

// These commands are pasted into a terminal on a machine that has nothing on
// it yet. A wrong URL is a dead end for somebody with no other way to check.
func TestTheInstallRecipePointsAtARealRelease(t *testing.T) {
	got := installRecipes("0.1.9")

	var pi *recipe
	for i := range got {
		if got[i].ID == "linux_arm64" {
			pi = &got[i]
		}
	}
	if pi == nil {
		t.Fatal("no recipe for a 64-bit Raspberry Pi, the machine this was written for")
	}
	want := "https://github.com/phil9922/backup-maker/releases/download/v0.1.9/backup-maker_0.1.9_linux_arm64.tar.gz"
	if !strings.Contains(pi.Commands[0], want) {
		t.Errorf("download line is %q,\nwant it to contain %q", pi.Commands[0], want)
	}
	if !strings.Contains(strings.Join(pi.Commands, "\n"), "autostart enable") {
		t.Error("the recipe never makes the install survive a reboot")
	}
	if !strings.Contains(strings.Join(pi.Commands, "\n"), "enable-linger") {
		t.Error("a headless machine needs linger, or the service dies at logout")
	}
	if pi.Note == "" {
		t.Error("the linger line is the one nobody understands; it needs its note")
	}
}

// A build that is not a release has no matching asset, so the recipe must not
// hand out a URL that 404s.
func TestTheInstallRecipeForADevBuildSendsYouToTheReleasesPage(t *testing.T) {
	for _, r := range installRecipes("") {
		joined := strings.Join(r.Commands, "\n")
		if strings.Contains(joined, "/download/v/") || strings.Contains(joined, "_._") {
			t.Fatalf("%s builds a download URL out of an empty version: %q", r.ID, joined)
		}
		if !strings.Contains(joined, "releases/latest") {
			t.Errorf("%s does not point anywhere a person could get the file: %q", r.ID, joined)
		}
	}
}

// Every line is pasted into a shell, so a "#" anywhere on a line makes the rest
// of that line do nothing. An explanation wedged into the middle of a command
// silently deletes the half after it — which is exactly what happened: a
// dev-build recipe read
//
//	curl -fsSL <page>  # pick the linux_arm64 archive | tar -xz backup-maker
//
// and the pipe that unpacked the download was commented out. Caught by running
// it against the real daemon, not by a test, so here is the test.
func TestNoRecipeLineHidesWorkBehindAComment(t *testing.T) {
	for _, version := range []string{"0.1.9", ""} {
		for _, r := range installRecipes(version) {
			for _, line := range r.Commands {
				h := strings.Index(line, "#")
				if h > 0 && strings.TrimSpace(line[:h]) != "" {
					t.Errorf("%s (version %q) has a command with a comment on the same line, so everything after the # is silently dropped:\n  %s", r.ID, version, line)
				}
			}
		}
	}
}

// A comment-only line explains; it must never be the only thing there, or the
// recipe explains without ever doing anything.
func TestEveryRecipeActuallyRunsSomething(t *testing.T) {
	for _, version := range []string{"0.1.9", ""} {
		for _, r := range installRecipes(version) {
			runs := 0
			for _, line := range r.Commands {
				if !strings.HasPrefix(strings.TrimSpace(line), "#") {
					runs++
				}
			}
			if runs == 0 {
				t.Errorf("%s (version %q) is all comments and never does anything", r.ID, version)
			}
		}
	}
}

// A dirty build reports the version of the tag it was built from. Pinning to
// that would tell somebody to install the RELEASED version while this machine
// runs that version plus changes — so the machine they set up would be missing
// the very features being used to set it up. Found by running it, not by a
// test: a local build reported 0.1.9 and would have handed out 0.1.9.
func TestAModifiedBuildPinsNoVersion(t *testing.T) {
	for _, tc := range []struct {
		name  string
		dirty bool
		ver   string
		want  string
	}{
		{"an unmodified release", false, "0.1.9", "0.1.9"},
		{"a release with a v prefix", false, "v0.1.9", "0.1.9"},
		{"the same release, modified", true, "0.1.9", ""},
		{"a build outside a release", false, "dev", ""},
		{"a pseudo-version", false, "0.0.0-20260725011938-ee7e25a6dbb4", ""},
		{"nothing at all", false, "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := pinnedVersion(tc.ver, tc.dirty); got != tc.want {
				t.Errorf("pinnedVersion(%q, dirty=%v) = %q, want %q", tc.ver, tc.dirty, got, tc.want)
			}
		})
	}
}

// Every platform the project publishes for should be offered, because the one
// thing the dashboard cannot work out is which of them the other machine is.
func TestEveryPublishedPlatformIsOffered(t *testing.T) {
	want := []string{"linux_arm64", "linux_amd64", "linux_armv7", "darwin_arm64", "darwin_amd64", "windows_amd64"}
	got := map[string]bool{}
	for _, r := range installRecipes("0.1.9") {
		got[r.ID] = true
		if r.Label == "" || len(r.Commands) == 0 {
			t.Errorf("%s has nothing usable in it", r.ID)
		}
	}
	for _, id := range want {
		if !got[id] {
			t.Errorf("no recipe for %s, which the project publishes a binary for", id)
		}
	}
}
