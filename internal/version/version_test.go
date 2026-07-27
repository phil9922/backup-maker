// SPDX-License-Identifier: MIT

package version

import (
	"strings"
	"testing"
)

func TestReleased(t *testing.T) {
	cases := []struct {
		in   string
		want bool
	}{
		{"v0.1.0", true},
		{"0.1.0", true},
		{"v1.2.3-rc1", true},
		{"v2.0.0+incompatible", true},

		{"", false},
		{"(devel)", false},
		// What Go synthesizes for a build that isn't at a tag — the case that
		// leaked a pseudo-version into `backup-maker --version`.
		{"v0.0.0-20260725011938-ee7e25a6dbb4", false},
		{"v0.0.0-20260725011938-ee7e25a6dbb4+dirty", false},
		{"0.0.0-20260725011938-ee7e25a6dbb4+dirty", false},
	}
	for _, c := range cases {
		if got := released(c.in); got != c.want {
			t.Errorf("released(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}

func TestGetFallsBackToDev(t *testing.T) {
	// The test binary is built without -ldflags, so the injected values are
	// empty and Get must not invent a version.
	i := Get()
	if i.Version == "" {
		t.Fatal("Version is empty; Get should fall back to \"dev\"")
	}
	if strings.Contains(i.Version, "+") || pseudoVersionRe.MatchString(i.Version) {
		t.Errorf("Version = %q; a pseudo-version must not be reported as a release", i.Version)
	}
	if i.Go == "" || i.OS == "" || i.Arch == "" {
		t.Errorf("runtime details incomplete: %+v", i)
	}
}

func TestShortReportsDirtyOnce(t *testing.T) {
	if got := strings.Count(Short(), "-dirty"); got > 1 {
		t.Errorf("Short() = %q, contains %d \"-dirty\" suffixes, want at most 1", Short(), got)
	}
}

func TestInfoString(t *testing.T) {
	i := Info{Version: "0.1.0", Commit: "ee7e25a6dbb4d6541eca722a62c7acc8b60f3c04", Go: "go1.26.5", OS: "linux", Arch: "amd64"}
	got := i.String()
	// The commit is abbreviated so the line stays readable.
	if !strings.Contains(got, "(ee7e25a6dbb4)") {
		t.Errorf("String() = %q, want an abbreviated commit", got)
	}
	if strings.Contains(got, "-dirty") {
		t.Errorf("String() = %q, want no dirty marker for a clean build", got)
	}
	for _, want := range []string{"backup-maker", "0.1.0", "linux/amd64", "go1.26.5"} {
		if !strings.Contains(got, want) {
			t.Errorf("String() = %q, missing %q", got, want)
		}
	}
}
