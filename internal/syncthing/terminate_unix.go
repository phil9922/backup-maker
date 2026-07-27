// SPDX-License-Identifier: MIT

//go:build !windows

package syncthing

import (
	"os/exec"
	"syscall"
)

// terminate asks the child to shut down cleanly; cmd.WaitDelay force-kills if
// it lingers.
func terminate(cmd *exec.Cmd) error {
	return cmd.Process.Signal(syscall.SIGTERM)
}
