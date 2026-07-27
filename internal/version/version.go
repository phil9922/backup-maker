// SPDX-License-Identifier: MIT

// Package version reports which build of backup-maker is running.
//
// The values below are set at link time by release builds (see
// .goreleaser.yaml). A plain "go build" leaves them empty, so anything that is
// recoverable from the binary's embedded build info is filled in from there
// instead — which means a locally built binary still reports its commit.
package version

import (
	"fmt"
	"regexp"
	"runtime"
	"runtime/debug"
	"strings"
)

// Go synthesizes a pseudo-version like "v0.0.0-20260725011938-ee7e25a6dbb4"
// for a build that isn't at a tag. That is not a released version, so it is
// rejected in favour of "dev".
var pseudoVersionRe = regexp.MustCompile(`^v?\d+\.\d+\.\d+-\d{14}-[0-9a-f]{12}$`)

// released reports whether a version recorded in the build info names an
// actual release, rather than a pseudo-version or an unversioned build.
func released(v string) bool {
	// Drop build metadata ("+dirty", "+incompatible") before judging.
	if i := strings.IndexByte(v, '+'); i >= 0 {
		v = v[:i]
	}
	if v == "" || v == "(devel)" {
		return false
	}
	return !pseudoVersionRe.MatchString(v)
}

// Set with -ldflags "-X github.com/phil9922/backup-maker/internal/version.<name>=..."
var (
	version = ""
	commit  = ""
	date    = ""
)

// Info describes the running build.
type Info struct {
	Version string // "0.1.0", or "dev" when built outside a release
	Commit  string // full git SHA, empty if unknown
	Date    string // RFC3339 build time, empty if unknown
	Dirty   bool   // built from a tree with uncommitted changes
	Go      string
	OS      string
	Arch    string
}

// Get describes the running build, preferring linker-injected values and
// falling back to the embedded build info.
func Get() Info {
	i := Info{
		Version: version,
		Commit:  commit,
		Date:    date,
		Go:      runtime.Version(),
		OS:      runtime.GOOS,
		Arch:    runtime.GOARCH,
	}

	if bi, ok := debug.ReadBuildInfo(); ok {
		if i.Version == "" && released(bi.Main.Version) {
			v, _, _ := strings.Cut(bi.Main.Version, "+")
			i.Version = strings.TrimPrefix(v, "v")
		}
		for _, s := range bi.Settings {
			switch s.Key {
			case "vcs.revision":
				if i.Commit == "" {
					i.Commit = s.Value
				}
			case "vcs.time":
				if i.Date == "" {
					i.Date = s.Value
				}
			case "vcs.modified":
				i.Dirty = s.Value == "true"
			}
		}
	}

	if i.Version == "" {
		i.Version = "dev"
	}
	return i
}

// Short is the version on its own, for `backup-maker --version`.
func Short() string {
	i := Get()
	if i.Dirty {
		return i.Version + "-dirty"
	}
	return i.Version
}

// String is a one-line description of the build, suitable for logs and
// bug reports.
func (i Info) String() string {
	s := "backup-maker " + i.Version
	if i.Dirty {
		s += "-dirty"
	}
	if i.Commit != "" {
		short := i.Commit
		if len(short) > 12 {
			short = short[:12]
		}
		s += " (" + short + ")"
	}
	return fmt.Sprintf("%s %s/%s %s", s, i.OS, i.Arch, i.Go)
}
