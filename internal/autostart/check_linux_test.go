// SPDX-License-Identifier: MIT

//go:build linux

package autostart

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// install writes a unit straight to the path Check reads, without going through
// Enable — which would run systemctl against the machine running the tests.
func install(t *testing.T, unit string) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	path, err := unitPath()
	if err != nil {
		t.Fatal(err)
	}
	if unit == "" {
		return path // nothing installed
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(unit), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// THE FAILURE THIS GUARDS. The restart policy and the wedged-daemon watchdog
// live in the systemd unit, not in the binary, and only `autostart enable`
// rewrites that file. Upgrading and restarting therefore leaves a new daemon
// running under the old machine's rules, with every protection it advertises
// sitting inert and no symptom whatsoever — which is exactly what happened on
// the developer's own laptop on 2026-07-27, release notes open, for an hour.
//
// This is the unit shipped before that fix, verbatim in shape: a daemon that
// exits cleanly stays stopped, and a wedged one is never noticed.
func TestAUnitFromBeforeTheWatchdogIsReportedOutOfDate(t *testing.T) {
	old := `[Unit]
Description=backup-maker continuous backups
After=network.target

[Service]
ExecStart=/home/pk/.local/bin/backup-maker daemon
Restart=on-failure
RestartSec=5

[Install]
WantedBy=default.target
`
	install(t, old)

	r, err := Check()
	if err != nil {
		t.Fatal(err)
	}
	if !r.Installed {
		t.Fatal("an installed unit was not seen at all")
	}
	if !r.NeedsReinstall() {
		t.Error("a unit with no WatchdogSec and Restart=on-failure was called up to date: " +
			"the user would never be told their recovery machinery is switched off")
	}
}

// The healthy case: what this binary writes must read as current, or the
// warning fires on every machine for ever and stops meaning anything.
func TestTheUnitThisVersionWritesIsUpToDate(t *testing.T) {
	install(t, unitText("/home/pk/.local/bin/backup-maker"))

	r, err := Check()
	if err != nil {
		t.Fatal(err)
	}
	if !r.Installed || !r.UpToDate {
		t.Errorf("the unit this version installs did not compare equal to itself: %+v", r)
	}
	if r.NeedsReinstall() {
		t.Error("a freshly installed unit asked the user to reinstall it")
	}
}

// The daemon runs from ~/.local/bin, but the same binary gets invoked from a
// build tree, a download folder, /tmp. If any of those read as "out of date",
// the message becomes noise and gets ignored — which costs more than it saves,
// because this warning has to be believed on the one day it matters.
func TestRunningTheBinaryFromElsewhereIsNotStaleness(t *testing.T) {
	install(t, unitText("/home/pk/.local/bin/backup-maker"))

	r, err := Check()
	if err != nil {
		t.Fatal(err)
	}
	if r.NeedsReinstall() {
		t.Error("a unit pointing at a different path than the running binary was called out of date")
	}
}

// The comments in this unit are long, deliberate, and get edited. Rewording one
// changes nothing about what systemd does, and must not tell every user in the
// world to reinstall their service.
func TestRewordingACommentIsNotAChange(t *testing.T) {
	unit := unitText("/home/pk/.local/bin/backup-maker")
	reworded := strings.Replace(unit,
		"# always, not on-failure: a backup daemon that exits cleanly for any reason —",
		"# Some entirely different explanation of the same directive.",
		1)
	if reworded == unit {
		t.Fatal("the comment this test rewrites is no longer in the unit")
	}
	install(t, reworded)

	r, err := Check()
	if err != nil {
		t.Fatal(err)
	}
	if r.NeedsReinstall() {
		t.Error("rewording a comment was reported as an out-of-date service definition")
	}
}

// Changing what systemd is actually told, however, must be caught — this is the
// half that makes the test above safe to have.
func TestChangingADirectiveIsReported(t *testing.T) {
	unit := unitText("/home/pk/.local/bin/backup-maker")
	weakened := strings.Replace(unit, "WatchdogSec=180", "WatchdogSec=6000", 1)
	if weakened == unit {
		t.Fatal("WatchdogSec is no longer in the unit")
	}
	install(t, weakened)

	r, err := Check()
	if err != nil {
		t.Fatal(err)
	}
	if !r.NeedsReinstall() {
		t.Error("a changed WatchdogSec was not noticed")
	}
}

// Never having run `autostart enable` is a choice, not a fault. Nagging someone
// who deliberately runs the daemon by hand is how a warning gets tuned out.
func TestNoServiceInstalledIsNotAComplaint(t *testing.T) {
	install(t, "")

	r, err := Check()
	if err != nil {
		t.Fatalf("checking an uninstalled service should not error: %v", err)
	}
	if r.Installed {
		t.Error("reported a service that was never installed")
	}
	if r.NeedsReinstall() {
		t.Error("asked a user with no service installed to reinstall it")
	}
}

// A unit at our path that we plainly did not write — hand-rolled, or from some
// other tool — gets no opinion rather than a wrong one.
func TestAUnitWeDidNotWriteIsNotJudged(t *testing.T) {
	install(t, "[Unit]\nDescription=something else entirely\n")

	r, err := Check()
	if err != nil {
		t.Fatal(err)
	}
	if r.NeedsReinstall() {
		t.Error("a unit with no ExecStart was judged against ours")
	}
}
