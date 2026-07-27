// SPDX-License-Identifier: MIT

//go:build darwin

package autostart

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

const agentLabel = "com.backup-maker.agent"

func plistPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, "Library", "LaunchAgents", agentLabel+".plist"), nil
}

func Enable() error {
	exe, err := exePath()
	if err != nil {
		return err
	}
	path, err := plistPath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	plist := plistText(exe)
	if err := os.WriteFile(path, []byte(plist), 0o644); err != nil {
		return err
	}
	uid := os.Getuid()
	_ = exec.Command("launchctl", "bootout", fmt.Sprintf("gui/%d", uid), path).Run() // reload if present
	if out, err := exec.Command("launchctl", "bootstrap", fmt.Sprintf("gui/%d", uid), path).CombinedOutput(); err != nil {
		return fmt.Errorf("launchctl bootstrap: %v: %s", err, out)
	}
	return nil
}

// plistText is the launch agent this version installs. Split out of Enable so
// Check can render the same thing and compare — see Report.
func plistText(exe string) string {
	return fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>Label</key><string>%s</string>
	<key>ProgramArguments</key>
	<array><string>%s</string><string>daemon</string></array>
	<key>RunAtLoad</key><true/>
	<!-- Plain true, not SuccessfulExit=false: a backup daemon that exits
	     cleanly has still stopped backing anything up, and the conditional
	     form would leave it stopped. -->
	<key>KeepAlive</key><true/>
</dict>
</plist>
`, agentLabel, exe)
}

// Check compares the installed launch agent with the one this binary would
// write. The program path is taken from the installed file for the same reason
// as on Linux: where the CLI was run from is not the question being asked.
func Check() (Report, error) {
	path, err := plistPath()
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
	exe := firstProgramArgument(string(installed))
	if exe == "" {
		// Not an agent we wrote: no opinion rather than a wrong one.
		r.Comparable = false
		return r, nil
	}
	// XML comments stripped on both sides: this plist carries an explanatory
	// one, and rewording it must not tell every macOS user they are stale.
	r.UpToDate = sameDefinition(
		stripXMLComments(string(installed)),
		stripXMLComments(plistText(exe)),
	)
	return r, nil
}

// firstProgramArgument returns the binary path out of ProgramArguments, which
// is the first <string> after that key. Returns "" if the shape is not ours.
func firstProgramArgument(plist string) string {
	i := strings.Index(plist, "<key>ProgramArguments</key>")
	if i < 0 {
		return ""
	}
	rest := plist[i:]
	open := strings.Index(rest, "<string>")
	if open < 0 {
		return ""
	}
	rest = rest[open+len("<string>"):]
	end := strings.Index(rest, "</string>")
	if end < 0 {
		return ""
	}
	return rest[:end]
}

func Disable() error {
	path, err := plistPath()
	if err != nil {
		return err
	}
	_ = exec.Command("launchctl", "bootout", fmt.Sprintf("gui/%d", os.Getuid()), path).Run()
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

func Status() (string, error) {
	path, err := plistPath()
	if err != nil {
		return "", err
	}
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return "not installed", nil
	}
	return "installed (launchd agent)", nil
}
