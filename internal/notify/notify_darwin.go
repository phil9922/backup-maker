// SPDX-License-Identifier: MIT

//go:build darwin

package notify

import (
	"context"
	"os/exec"
	"strings"
)

// send posts to Notification Center through osascript, which is present on
// every macOS install and needs no bundle, no dependency and no permission
// prompt of its own.
//
// BE STRAIGHT ABOUT THE LIMIT: there is no sticky notification down this route.
// Whether a notification banners-and-fades or stays as an alert is a per-app
// setting the USER owns in System Settings > Notifications, and this route is
// attributed to Script Editor, not to us. So a critical backup failure fades
// here the same as anything else. It is still worth sending — it is also kept
// in Notification Center, where it can be found later — but on macOS the
// destination status page and the dashboard remain the reliable answer.
func send(ctx context.Context, u Urgency, title, body string) error {
	// AppleScript string literals cannot span lines, so the text is flattened
	// before it is quoted.
	script := "display notification " + quote(body) + " with title " + quote(title)
	return run(ctx, "osascript", "-e", script)
}

// hideConsole is a Windows concern; there is no window to hide here.
func hideConsole(*exec.Cmd) {}

// quote renders a Go string as an AppleScript string literal.
func quote(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `"`, `\"`)
	return `"` + r.Replace(oneLine(s)) + `"`
}
