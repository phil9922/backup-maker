// SPDX-License-Identifier: MIT

package machines

import "syscall"

// wsaeconnrefused is Winsock's "connection refused" — what connectex returns
// when nothing is listening.
//
// SPELLED OUT AS A NUMBER because Go's syscall package exports no name for it on
// Windows (syscall.WSAECONNREFUSED does not exist, which cost a CI run to
// learn). 10061 is fixed by Winsock and is not going to move.
const wsaeconnrefused = syscall.Errno(10061)

// connRefused is Windows's spelling of "nothing was listening". See the
// non-Windows file beside this one for why the test cannot simply use
// ECONNREFUSED everywhere.
func connRefused() error { return wsaeconnrefused }
