// SPDX-License-Identifier: MIT

// Package drivesetup is the daemon's side of preparing a drive.
//
// The daemon runs as an ordinary user and never gains privilege itself.
// Partitioning a disk needs root, so this package does exactly one thing: it
// re-enters the same binary as root, through sudo, to run the prepare-drive
// subcommand — where every guard lives. If sudo will not do that without a
// password, nothing here tries to work around it. It reports that it cannot,
// and Manual returns the commands for the user to run themselves.
//
// Keeping the escalation in one small package is the point: there is a single
// place to read to know everything the dashboard can cause to happen as root.
package drivesetup

import (
	"encoding/json"
	"strings"
)

// Request is one drive to prepare. Confirm is the phrase the user typed back,
// checked against the drive itself by the privileged side.
type Request struct {
	Device  string `json:"device"`
	Mount   string `json:"mount"`
	Label   string `json:"label"`
	Confirm string `json:"confirm"`
	// Probe asks the privileged side to change nothing and exit 0. It is how
	// the daemon finds out whether sudo will run the command without a
	// password, and it travels in the request precisely so that ASKING costs
	// the same single exact command that DOING does — see PrivilegedArgs.
	Probe bool `json:"probe,omitempty"`
}

// PrivilegedArgs is the one command the dashboard is ever allowed to run as
// root, and it takes no request arguments at all.
//
// THE ARGUMENTS ARE THE ATTACK SURFACE. This used to be the full flag form, so
// the sudoers rule ended in a wildcard — and a sudoers wildcard matches any
// further arguments, whitespace included. That handed anything running as this
// user a passwordless `prepare-drive --force …`, which skips the "there is
// already something on it" refusal: the single guard the whole design leans on.
// With no arguments to match, the rule is exact and there is nothing to inject.
//
// The request travels on stdin instead, where sudo's rule cannot be widened by
// it. --force has no field in Request and so cannot be reached this way at all;
// it stays available to somebody who types their own password at a terminal.
func PrivilegedArgs() []string {
	return []string{"prepare-drive", "--from-stdin"}
}

// Encode renders the request for the privileged side's stdin.
func (r Request) Encode() ([]byte, error) { return json.Marshal(r) }

// Args is the prepare-drive command line a PERSON would type, used for the
// copy-paste instructions. Never for the sudo invocation: see PrivilegedArgs.
func (r Request) Args() []string {
	return []string{
		"prepare-drive",
		"--device", r.Device,
		"--mount", r.Mount,
		"--label", r.Label,
		"--confirm", r.Confirm,
	}
}

// quoted renders the arguments for a human to paste into a shell.
func quoted(argv []string) string {
	out := make([]string, 0, len(argv))
	for _, a := range argv {
		if strings.ContainsAny(a, " \t\"'\\$") {
			out = append(out, `"`+strings.ReplaceAll(a, `"`, `\"`)+`"`)
			continue
		}
		out = append(out, a)
	}
	return strings.Join(out, " ")
}
