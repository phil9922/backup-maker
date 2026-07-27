// SPDX-License-Identifier: MIT

// Package notify shows desktop notifications, so a backup that has stopped
// working reaches the user instead of waiting to be discovered.
//
// Everything here is BEST EFFORT and silent about its own failures. A headless
// machine — a Raspberry Pi acting as a backup target — has no screen and no
// notification daemon, and that is a perfectly normal way to run backup-maker,
// not an error worth a log line at anything above debug. The same applies to a
// missing helper binary or a desktop that answers with a non-zero exit. Nothing
// in this package may block its caller for long, fail a backup, or panic.
package notify

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// Urgency is how hard a notification tries to interrupt.
type Urgency int

const (
	// Normal appears and fades on its own. For news that is good, or that can
	// wait — anything the user would resent having to dismiss.
	Normal Urgency = iota
	// Critical is for "your backups are not working". On desktops that honour
	// it (Linux, via notify-send -u critical) it stays on screen until it is
	// dismissed; a notification that fades while nobody is looking at the
	// screen has not told anyone anything.
	Critical
)

// appName is what the desktop attributes the notification to.
const appName = "backup-maker"

// execTimeout bounds a helper. Generous, because a first D-Bus activation of a
// notification daemon is not instant — but finite, because on a box with a
// session bus and no notification service, notify-send waits out the D-Bus
// timeout (25s) before giving up, and nothing here may hold a caller that long.
const execTimeout = 10 * time.Second

// Notifier delivers one desktop notification. The error is for a debug log and
// nothing else: every caller is expected to carry on regardless.
type Notifier interface {
	Notify(ctx context.Context, u Urgency, title, body string) error
}

// Desktop returns the notifier for this operating system. On a machine with no
// graphical session it is a quiet no-op.
func Desktop() Notifier { return desktop{} }

type desktop struct{}

func (desktop) Notify(ctx context.Context, u Urgency, title, body string) error {
	ctx, cancel := context.WithTimeout(ctx, execTimeout)
	defer cancel()
	return send(ctx, u, title, body)
}

// run executes a notification helper under ctx's deadline.
//
// A missing binary is reported as an ordinary error rather than searched for
// again: on a headless machine notify-send simply is not installed, and that
// costs one failed lookup per alert, not per minute.
//
// WaitDelay is what makes the deadline real. Killing the process is not enough
// on its own — Wait also waits for whatever holds the output pipes, so a helper
// that leaves a child behind could otherwise hang here for ever.
func run(ctx context.Context, name string, args ...string) error {
	path, err := exec.LookPath(name)
	if err != nil {
		return err
	}
	cmd := exec.CommandContext(ctx, path, args...)
	cmd.WaitDelay = 2 * time.Second
	hideConsole(cmd)
	out, err := cmd.CombinedOutput()
	if err != nil {
		if trimmed := strings.TrimSpace(string(out)); trimmed != "" {
			return fmt.Errorf("%s: %w: %s", name, err, trimmed)
		}
		return fmt.Errorf("%s: %w", name, err)
	}
	return nil
}

// oneLine flattens text for helpers whose argument is a command line rather
// than an argv entry.
func oneLine(s string) string {
	return strings.Join(strings.Fields(s), " ")
}
