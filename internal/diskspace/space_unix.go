// SPDX-License-Identifier: MIT

//go:build linux || darwin

// Package diskspace reports free space on a locally mounted path. It is shared
// by the drive picker (which shows "52GB free of 64GB") and by the reclaimer
// (which needs to know when a destination is running out of room).
package diskspace

import "syscall"

// Usage returns the bytes available to this user and the total size of the
// filesystem holding path.
//
// Available, not free: on filesystems with reserved blocks the difference is
// real, and reporting space we cannot actually write would make the reclaimer
// give up too late.
func Usage(path string) (avail, total uint64, err error) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return 0, 0, err
	}
	bs := uint64(st.Bsize)
	return st.Bavail * bs, st.Blocks * bs, nil
}
