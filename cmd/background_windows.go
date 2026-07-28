// SPDX-License-Identifier: MIT

//go:build windows

package cmd

import (
	"os"
	"os/exec"
	"syscall"
)

// relaunchDetached restarts the daemon with no console window (used by the
// Windows login autostart entry).
func relaunchDetached() error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	c := exec.Command(exe, "daemon")
	c.SysProcAttr = &syscall.SysProcAttr{
		HideWindow:    true,
		CreationFlags: 0x08000000 | 0x00000008, // CREATE_NO_WINDOW | DETACHED_PROCESS
	}
	return c.Start()
}
