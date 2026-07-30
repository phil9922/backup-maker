// SPDX-License-Identifier: MIT

//go:build linux

package autostart

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

const unitName = "backup-maker.service"

func unitPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".config", "systemd", "user", unitName), nil
}

// RestartsRunningDaemon reports whether Enable interrupts a daemon that is
// already running. It does here: Enable restarts the service deliberately, so a
// running copy picks up the unit it just wrote rather than keeping the old one.
// The CLI says so, because a command that sounds purely declarative should not
// silently stop and start the thing protecting your files.
const RestartsRunningDaemon = true

// Enable installs and starts a systemd user unit. Headless machines
// additionally need `loginctl enable-linger` (documented, not automated).
func Enable() error {
	exe, err := exePath()
	if err != nil {
		return err
	}
	path, err := unitPath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	unit := unitText(exe)
	if err := os.WriteFile(path, []byte(unit), 0o644); err != nil {
		return err
	}
	if out, err := exec.Command("systemctl", "--user", "daemon-reload").CombinedOutput(); err != nil {
		return fmt.Errorf("systemctl daemon-reload: %v: %s", err, out)
	}
	if out, err := exec.Command("systemctl", "--user", "enable", unitName).CombinedOutput(); err != nil {
		return fmt.Errorf("systemctl enable: %v: %s", err, out)
	}
	// restart, not "enable --now": --now starts a stopped service but leaves a
	// running one alone, so an already-running daemon would keep the OLD unit
	// definition and quietly ignore anything changed above — a new watchdog or
	// restart policy would appear in the file and do nothing. Restarting is
	// safe: copies write to a temp file and rename, so nothing is left half
	// written, and the mirror resumes where it stopped.
	if out, err := exec.Command("systemctl", "--user", "restart", unitName).CombinedOutput(); err != nil {
		return fmt.Errorf("systemctl restart: %v: %s", err, out)
	}
	return nil
}

// unitText is the unit this version of backup-maker installs. It is a function
// rather than inline in Enable so that Check can render the same thing and
// compare — the installed file is the only place these protections actually
// live, and a stale one is invisible without something to hold it against.
func unitText(exe string) string {
	return fmt.Sprintf(`[Unit]
Description=backup-maker continuous backups
After=network.target

[Service]
ExecStart=%s daemon
# always, not on-failure: a backup daemon that exits cleanly for any reason —
# an unhandled shutdown path, a signal, something odd in the environment — has
# still stopped backing anything up. on-failure would leave it stopped and say
# nothing. An explicit "systemctl --user stop" is still honoured.
Restart=always
RestartSec=5
# The other half of Restart=always: a daemon that DEADLOCKS never exits, so
# without this it would sit at "active (running)" for ever, backing nothing up.
# It reports in every ten seconds for as long as its central lock can still be
# taken; three minutes of silence means it is wedged and systemd restarts it.
#
# Type stays simple. Type=notify would make systemd wait for a READY=1 at
# startup and hang the service if it never arrived — a worse failure than the
# one being fixed. NotifyAccess=main is all a watchdog ping needs: it sets
# $NOTIFY_SOCKET in the environment and accepts messages from the main process,
# which is where the ping comes from.
WatchdogSec=180
NotifyAccess=main

[Install]
WantedBy=default.target
`, exe)
}

// Check compares the installed unit with the one this binary would write.
//
// The ExecStart path is taken FROM THE INSTALLED FILE rather than from this
// process, deliberately. The daemon normally runs from ~/.local/bin while the
// same binary may be invoked from a build tree or a download directory, and a
// check that called every one of those "out of date" would be noise — the
// question is whether the service's instructions are current, not where the
// user happened to run the CLI from.
func Check() (Report, error) {
	path, err := unitPath()
	if err != nil {
		return Report{}, err
	}
	r := Report{Comparable: true, Path: path}
	installed, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return r, nil // not installed: a choice, not a fault
	}
	if err != nil {
		return Report{Path: path}, err
	}
	r.Installed = true
	exe := execStartPath(string(installed))
	if exe == "" {
		// No ExecStart at all: whatever this is, it is not a unit we wrote, and
		// there is nothing honest to say about it. Not comparable rather than
		// out of date — telling someone to overwrite a file we do not
		// understand is the one outcome worse than staying quiet.
		r.Comparable = false
		return r, nil
	}
	r.UpToDate = sameDefinition(string(installed), unitText(exe))
	return r, nil
}

// execStartPath pulls the binary out of an ExecStart line, without its
// arguments. Returns "" when there is no ExecStart to find.
func execStartPath(unit string) string {
	for _, line := range strings.Split(unit, "\n") {
		line = strings.TrimSpace(line)
		rest, ok := strings.CutPrefix(line, "ExecStart=")
		if !ok {
			continue
		}
		// Strip systemd's leading modifiers (-, @, +, !) before the path.
		rest = strings.TrimLeft(rest, "-@+!")
		if i := strings.Index(rest, " "); i >= 0 {
			rest = rest[:i]
		}
		return rest
	}
	return ""
}

func Disable() error {
	_ = exec.Command("systemctl", "--user", "disable", "--now", unitName).Run()
	path, err := unitPath()
	if err != nil {
		return err
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	_ = exec.Command("systemctl", "--user", "daemon-reload").Run()
	return nil
}

func Status() (string, error) {
	out, _ := exec.Command("systemctl", "--user", "is-enabled", unitName).CombinedOutput()
	state := string(out)
	active, _ := exec.Command("systemctl", "--user", "is-active", unitName).CombinedOutput()
	return fmt.Sprintf("enabled: %s active: %s", trim(state), trim(string(active))), nil
}

func trim(s string) string {
	for len(s) > 0 && (s[len(s)-1] == '\n' || s[len(s)-1] == '\r') {
		s = s[:len(s)-1]
	}
	return s
}
