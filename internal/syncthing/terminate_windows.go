// SPDX-License-Identifier: MIT

//go:build windows

package syncthing

import "os/exec"

// Windows has no SIGTERM; syncthing handles abrupt kills safely (all writes
// are atomic-rename based).
func terminate(cmd *exec.Cmd) error {
	return cmd.Process.Kill()
}
