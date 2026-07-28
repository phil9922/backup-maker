// SPDX-License-Identifier: MIT

//go:build !linux

package watchdog

import "time"

// open reports no watchdog. sd_notify is systemd's protocol and systemd is
// Linux-only; macOS launchd and the Windows service manager have no equivalent
// keepalive, so on those platforms the whole feature compiles to a return.
func open() (Notifier, time.Duration, error) { return nil, 0, nil }
