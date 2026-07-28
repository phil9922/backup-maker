// SPDX-License-Identifier: MIT

// Package autostart installs backup-maker as a login-time background service,
// per OS, without requiring admin rights.
package autostart

import (
	"os"
	"strings"
)

// exePath returns the absolute path of the current binary for service files.
func exePath() (string, error) {
	return os.Executable()
}

// Report describes the service definition installed on this machine as against
// the one this binary would write today.
//
// WHY THIS EXISTS. The protections that keep backups running — restart policy,
// the watchdog that catches a wedged daemon — live in the SERVICE DEFINITION,
// not in the binary. Nothing rewrites that file except `autostart enable`. So
// upgrading backup-maker and restarting it leaves a new version running under
// the old rules, with every protection it advertises sitting inert, and no
// symptom until the day something goes wrong and nothing recovers. It cost a
// real hour on 2026-07-27, on the developer's own machine, with the release
// notes open. The daemon cannot fix this for the user — rewriting a unit file
// behind their back is worse — but it can stop it being invisible.
type Report struct {
	// Installed is false when no service definition is present at all: the
	// user never ran `autostart enable`, which is a choice, not a fault.
	Installed bool
	// UpToDate is true when what is installed says the same thing this binary
	// would write. Meaningless unless Installed and Comparable are both true.
	UpToDate bool
	// Comparable is false where this OS has no supported way to answer —
	// reported as "cannot tell" rather than quietly as "fine", because a
	// false all-clear here is the exact failure the whole check is about.
	Comparable bool
	// Path is the service definition inspected, for a message the user can act on.
	Path string
}

// NeedsReinstall reports whether the user should be told to run
// `backup-maker autostart enable`. Deliberately false when nothing is
// installed, and false when this OS cannot be checked: telling someone to
// re-run a command on no evidence trains them to ignore the message.
func (r Report) NeedsReinstall() bool {
	return r.Installed && r.Comparable && !r.UpToDate
}

// sameDefinition compares two service definitions by what they instruct, not by
// how they read: blank lines, indentation and comments are dropped first. That
// keeps a reworded comment — this package's are long, and get edited — from
// telling every user in the world that their service is out of date.
func sameDefinition(a, b string) bool {
	return strings.Join(directives(a), "\n") == strings.Join(directives(b), "\n")
}

// stripXMLComments removes <!-- ... --> blocks, so that the explanatory comment
// in the launch agent can be reworded without every macOS user being told their
// service is out of date. An unterminated comment swallows the rest, which is
// the safe direction: it can only make two files compare equal, never make an
// identical pair differ.
func stripXMLComments(s string) string {
	var b strings.Builder
	for {
		i := strings.Index(s, "<!--")
		if i < 0 {
			b.WriteString(s)
			return b.String()
		}
		b.WriteString(s[:i])
		rest := s[i+4:]
		j := strings.Index(rest, "-->")
		if j < 0 {
			return b.String()
		}
		s = rest[j+3:]
	}
}

func directives(s string) []string {
	var out []string
	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, ";") {
			continue
		}
		out = append(out, line)
	}
	return out
}
