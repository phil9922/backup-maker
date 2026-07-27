// SPDX-License-Identifier: MIT

//go:build linux

package autostart

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

const unitName = "backup-maker.service"

func unitPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".config", "systemd", "user", unitName), nil
}

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
	unit := fmt.Sprintf(`[Unit]
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
