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
