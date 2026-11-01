// SPDX-License-Identifier: MIT

package archive

import (
	"os"

	"golang.org/x/sys/windows"
)

// markSparse sets FSCTL_SET_SPARSE so a later Truncate leaves holes instead of
// allocating the whole length on disk.
func markSparse(f *os.File) error {
	var n uint32
	return windows.DeviceIoControl(windows.Handle(f.Fd()), windows.FSCTL_SET_SPARSE, nil, 0, nil, 0, &n, nil)
}
