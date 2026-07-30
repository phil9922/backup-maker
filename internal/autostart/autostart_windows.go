// SPDX-License-Identifier: MIT

//go:build windows

package autostart

import (
	"fmt"
	"strings"

	"golang.org/x/sys/windows/registry"
)

const runValue = "backup-maker"

// RestartsRunningDaemon reports whether Enable interrupts a daemon that is
// already running. It does NOT here: Enable only writes an HKCU Run entry, which
// takes effect at the next sign-in. A running daemon is left alone, so the CLI
// must not claim it was restarted.
const RestartsRunningDaemon = false

// Enable adds an HKCU Run entry — the simplest no-admin autostart. The
// --background flag makes the daemon detach without a console window.
func Enable() error {
	exe, err := exePath()
	if err != nil {
		return err
	}
	key, err := registry.OpenKey(registry.CURRENT_USER,
		`Software\Microsoft\Windows\CurrentVersion\Run`, registry.SET_VALUE)
	if err != nil {
		return err
	}
	defer key.Close()
	return key.SetStringValue(runValue, runCommand(exe))
}

// runCommand is the Run entry this version installs. Split out of Enable so
// Check can render the same thing and compare — see Report.
func runCommand(exe string) string {
	return fmt.Sprintf(`"%s" daemon --background`, exe)
}

// Check compares the installed Run entry with the one this binary would write.
//
// There is less to go stale here than on Linux — an HKCU Run entry carries no
// restart policy and no watchdog, so what is being compared is the command and
// its flags. Saying so is still worth it: "up to date" must mean the same thing
// on every platform, and a --background that stopped being passed would leave a
// console window open at every login.
func Check() (Report, error) {
	r := Report{Comparable: true, Path: `HKCU\Software\Microsoft\Windows\CurrentVersion\Run\` + runValue}
	key, err := registry.OpenKey(registry.CURRENT_USER,
		`Software\Microsoft\Windows\CurrentVersion\Run`, registry.QUERY_VALUE)
	if err != nil {
		return Report{Path: r.Path}, err
	}
	defer key.Close()
	installed, _, err := key.GetStringValue(runValue)
	if err == registry.ErrNotExist {
		return r, nil // not installed: a choice, not a fault
	}
	if err != nil {
		return Report{Path: r.Path}, err
	}
	r.Installed = true
	exe := quotedExe(installed)
	if exe == "" {
		// Not an entry we wrote: no opinion rather than a wrong one.
		r.Comparable = false
		return r, nil
	}
	r.UpToDate = installed == runCommand(exe)
	return r, nil
}

// quotedExe returns the path inside the leading quotes of a Run command.
func quotedExe(cmd string) string {
	if !strings.HasPrefix(cmd, `"`) {
		return ""
	}
	rest := cmd[1:]
	i := strings.Index(rest, `"`)
	if i < 0 {
		return ""
	}
	return rest[:i]
}

func Disable() error {
	key, err := registry.OpenKey(registry.CURRENT_USER,
		`Software\Microsoft\Windows\CurrentVersion\Run`, registry.SET_VALUE)
	if err != nil {
		return err
	}
	defer key.Close()
	err = key.DeleteValue(runValue)
	if err == registry.ErrNotExist {
		return nil
	}
	return err
}

func Status() (string, error) {
	key, err := registry.OpenKey(registry.CURRENT_USER,
		`Software\Microsoft\Windows\CurrentVersion\Run`, registry.QUERY_VALUE)
	if err != nil {
		return "", err
	}
	defer key.Close()
	if _, _, err := key.GetStringValue(runValue); err != nil {
		return "not installed", nil
	}
	return "installed (login autostart)", nil
}
