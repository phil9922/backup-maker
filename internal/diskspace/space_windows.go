// SPDX-License-Identifier: MIT

// Package diskspace reports free space on a locally mounted path. It is shared
// by the drive picker (which shows "52GB free of 64GB") and by the reclaimer
// (which needs to know when a destination is running out of room).
package diskspace

import (
	"syscall"
	"unsafe"
)

var (
	kernel32        = syscall.NewLazyDLL("kernel32.dll")
	procGetDiskFree = kernel32.NewProc("GetDiskFreeSpaceExW")
)

// Usage returns the bytes available to this user and the total size of the
// volume holding path.
//
// The first out-parameter of GetDiskFreeSpaceExW is the caller's available
// space, which already accounts for per-user quotas — the number we actually
// need, rather than the volume-wide free figure.
func Usage(path string) (avail, total uint64, err error) {
	ptr, perr := syscall.UTF16PtrFromString(path)
	if perr != nil {
		return 0, 0, perr
	}
	var free, totalBytes, totalFree uint64
	r, _, callErr := procGetDiskFree.Call(
		uintptr(unsafe.Pointer(ptr)),
		uintptr(unsafe.Pointer(&free)),
		uintptr(unsafe.Pointer(&totalBytes)),
		uintptr(unsafe.Pointer(&totalFree)),
	)
	if r == 0 {
		return 0, 0, callErr
	}
	return free, totalBytes, nil
}
