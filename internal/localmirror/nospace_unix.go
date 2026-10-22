// SPDX-License-Identifier: MIT

//go:build linux || darwin

package localmirror

import (
	"errors"
	"syscall"
)

// isSystemNoSpace recognises a full local filesystem. os wraps the raw errno
// in *PathError, which errors.Is unwraps for us.
func isSystemNoSpace(err error) bool {
	return errors.Is(err, syscall.ENOSPC) || errors.Is(err, syscall.EDQUOT)
}

// isSystemReadOnly recognises a local filesystem that refuses writes — most
// often one the kernel remounted read-only after the disk under it failed.
func isSystemReadOnly(err error) bool {
	return errors.Is(err, syscall.EROFS)
}
