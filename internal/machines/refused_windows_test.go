// SPDX-License-Identifier: MIT

package machines

import "syscall"

// connRefused is Windows's spelling of "nothing was listening". See the
// non-Windows file beside this one for why the test cannot simply use
// ECONNREFUSED everywhere.
func connRefused() error { return syscall.WSAECONNREFUSED }
