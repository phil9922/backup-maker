// SPDX-License-Identifier: MIT

//go:build linux

package notify

import (
	"context"
	"errors"
	"os"
	"os/exec"
)

// errNoSession is the ordinary headless case: nothing to show a notification
// on. Returned rather than swallowed so a debug log can say why nothing
// appeared, but it is not a failure of anything.
var errNoSession = errors.New("no graphical session on this machine")

// send raises a notification through notify-send, which every mainstream Linux
// desktop provides (libnotify).
//
// Urgency is the whole mechanism for stickiness here — NOT the expire time.
// Tested on Cinnamon: `-u critical` stays on screen until it is dismissed,
// while `-t 0` fades like any other notification. So a critical alert is left
// with no -t at all and told to be critical.
//
// The daemon runs as a systemd USER service, which inherits DISPLAY and
// DBUS_SESSION_BUS_ADDRESS from the session it was started in, so notify-send
// finds the desktop without any help from us.
func send(ctx context.Context, u Urgency, title, body string) error {
	if !hasSession() {
		return errNoSession
	}
	urgency := "normal"
	if u == Critical {
		urgency = "critical"
	}
	args := []string{"-a", appName, "-u", urgency}
	// An icon makes the popup recognisable as this program's at a glance,
	// which matters for an alert the user is meant to act on. Omitted rather
	// than faked if it could not be unpacked: notify-send given a path that
	// is not there draws nothing useful.
	if icon := IconPath(); icon != "" {
		args = append(args, "-i", icon)
	}
	args = append(args, title, body)
	return run(ctx, "notify-send", args...)
}

// hideConsole is a Windows concern; there is no window to hide here.
func hideConsole(*exec.Cmd) {}

// hasSession reports whether there is plausibly a desktop to notify. Cheap
// guess, and only an optimisation: without it notify-send on a headless box
// would sit out the D-Bus timeout on every alert before failing anyway.
func hasSession() bool {
	for _, env := range []string{"DISPLAY", "WAYLAND_DISPLAY", "DBUS_SESSION_BUS_ADDRESS"} {
		if os.Getenv(env) != "" {
			return true
		}
	}
	return false
}
