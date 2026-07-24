// SPDX-License-Identifier: MIT

package localmirror

import (
	"errors"
	"syscall"
)

// ERROR_DISK_FULL and ERROR_HANDLE_DISK_FULL are what Windows returns when a
// write can't be satisfied; neither maps to a portable errno.
const (
	errorDiskFull       syscall.Errno = 112
	errorHandleDiskFull syscall.Errno = 39
)

func isSystemNoSpace(err error) bool {
	return errors.Is(err, errorDiskFull) || errors.Is(err, errorHandleDiskFull)
}
