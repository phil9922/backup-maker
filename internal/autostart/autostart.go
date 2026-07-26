// SPDX-License-Identifier: MIT

// Package autostart installs backup-maker as a login-time background service,
// per OS, without requiring admin rights.
package autostart

import "os"

// exePath returns the absolute path of the current binary for service files.
func exePath() (string, error) {
	return os.Executable()
}
