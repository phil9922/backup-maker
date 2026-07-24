// SPDX-License-Identifier: MIT

//go:build !windows

package cmd

import (
	"os"
	"os/exec"
)

// relaunchDetached restarts the daemon detached from this terminal. On
// Linux/macOS the service managers handle this, but the flag still works.
func relaunchDetached() error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	c := exec.Command(exe, "daemon")
	c.Stdout = nil
	c.Stderr = nil
	return c.Start()
}
