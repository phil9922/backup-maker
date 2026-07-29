// SPDX-License-Identifier: MIT

//go:build linux

package drivesetup

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
)

// Allowed reports whether this user can run prepare-drive as root without
// being asked for a password.
//
// It asks sudo rather than looking for the rule file, because a rule file is
// only one of the ways permission can be granted and sudo is what actually
// decides. It asks by RUNNING a do-nothing probe, not by listing:
//
//	sudo -l <command>   answers "is this command permitted?"
//	sudo -n <command>   answers "can I run it right now, without a password?"
//
// Only the second is the question worth asking. The first returns success for
// any member of the sudo group, so using it reported that the dashboard could
// format a drive on a machine where pressing the button could only ever
// produce "sudo: a password is required" — a button that promised something it
// could not do. Found by pressing it.
func Allowed() bool {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err := runPrivileged(ctx, Request{Probe: true})
	return err == nil
}

// runPrivileged re-enters this binary as root through sudo, with the request on
// stdin. The one place any of this becomes root.
//
// -n means sudo never prompts: if permission has not been granted the call
// fails immediately rather than hanging on a password prompt that nobody is
// there to answer.
func runPrivileged(ctx context.Context, r Request) (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("cannot work out where backup-maker lives: %w", err)
	}
	sudo, err := exec.LookPath("sudo")
	if err != nil {
		return "", errNoSudo
	}
	body, err := r.Encode()
	if err != nil {
		return "", err
	}
	cmd := exec.CommandContext(ctx, sudo, append([]string{"-n", exe}, PrivilegedArgs()...)...)
	cmd.Stdin = bytes.NewReader(body)
	out, err := cmd.CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

var errNoSudo = errors.New("sudo is not installed")

// Run prepares the drive by re-entering this binary as root.
//
// -n means sudo never prompts: if permission has not been granted the call
// fails immediately rather than hanging on a password prompt that nobody is
// there to answer. The privileged side re-checks everything about the request;
// nothing here is trusted by it.
func Run(ctx context.Context, r Request) (string, error) {
	r.Probe = false // a request to DO something is never a probe
	text, err := runPrivileged(ctx, r)
	if errors.Is(err, errNoSudo) {
		return "", fmt.Errorf("sudo is not installed, so the drive cannot be prepared from here. %s", manualHint(r))
	}
	if err != nil {
		// "sudo: a password is required" on its own tells the user nothing they
		// can act on from a web page, where there is nowhere to type one.
		// Recognised explicitly as well as via Allowed(), so this can never
		// again reach the screen as a bare sudo error.
		if needsPassword(text) || !Allowed() {
			return text, fmt.Errorf("this computer has not been set up to let the dashboard prepare a drive without a password. %s", manualHint(r))
		}
		if text == "" {
			text = err.Error()
		}
		return text, fmt.Errorf("%s", text)
	}
	return text, nil
}

func needsPassword(out string) bool {
	l := strings.ToLower(out)
	return strings.Contains(l, "password is required") || strings.Contains(l, "a terminal is required")
}

// Manual is what to tell somebody who has to do it themselves: the exact
// commands, for this drive, on this computer.
func Manual(r Request) []string {
	exe, err := os.Executable()
	if err != nil {
		exe = "backup-maker"
	}
	return []string{
		"sudo " + exe + " " + quoted(r.Args()),
	}
}

// CommandPrefix is what the dashboard shows above the button, so the command
// about to be run as root is visible before it is run — and is the same thing
// to paste by hand if permission has not been granted.
func CommandPrefix() string {
	exe, err := os.Executable()
	if err != nil {
		exe = "backup-maker"
	}
	return "sudo " + exe + " prepare-drive"
}

// AllowCommand is the one-off command that lets the dashboard do it instead.
func AllowCommand() string {
	exe, err := os.Executable()
	if err != nil {
		exe = "backup-maker"
	}
	return "sudo " + exe + " prepare-drive --install-sudoers"
}

func manualHint(r Request) string {
	return "Run this on that computer instead:\n  " + strings.Join(Manual(r), "\n  ") +
		"\n\nOr allow the dashboard to do it, once:\n  " + AllowCommand()
}
