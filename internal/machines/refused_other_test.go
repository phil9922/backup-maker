// SPDX-License-Identifier: MIT

//go:build !windows

package machines

import "syscall"

// connRefused is what the operating system reports when nothing is listening.
//
// SPLIT PER PLATFORM BECAUSE THE ERRNO IS. Windows returns WSAECONNREFUSED from
// connectex, which is a different value from ECONNREFUSED and does not compare
// equal to it — so the assertion that our error wrapping keeps the CAUSE
// reachable was failing on Windows for a reason that had nothing to do with the
// wrapping. What the test is really asking is "can a caller still tell that
// nothing was listening", and each platform spells that differently.
func connRefused() error { return syscall.ECONNREFUSED }
