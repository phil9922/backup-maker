// SPDX-License-Identifier: MIT

package browse

import (
	"os"
	"syscall"
	"unsafe"
)

var (
	kernel32          = syscall.NewLazyDLL("kernel32.dll")
	procGetLogical    = kernel32.NewProc("GetLogicalDrives")
	procGetDriveType  = kernel32.NewProc("GetDriveTypeW")
	procGetDiskFree   = kernel32.NewProc("GetDiskFreeSpaceExW")
	procGetVolumeInfo = kernel32.NewProc("GetVolumeInformationW")
)

// Drive types worth offering as a backup destination. DRIVE_REMOTE (4) is
// excluded deliberately: a mapped network drive is better added as a network
// target so backup-maker owns the connection and credentials.
const (
	driveRemovable = 2
	driveFixed     = 3
)

// Drives enumerates drive letters that are ready and writable.
func Drives() []Drive {
	mask, _, _ := procGetLogical.Call()
	var out []Drive
	for i := 0; i < 26; i++ {
		if mask&(1<<uint(i)) == 0 {
			continue
		}
		root := string(rune('A'+i)) + `:\`
		ptr, err := syscall.UTF16PtrFromString(root)
		if err != nil {
			continue
		}
		kind, _, _ := procGetDriveType.Call(uintptr(unsafe.Pointer(ptr)))
		if kind != driveRemovable && kind != driveFixed {
			continue
		}
		// An empty card reader reports a letter but can't be opened.
		if _, err := os.Stat(root); err != nil {
			continue
		}
		d := Drive{Path: root, Label: volumeLabel(ptr, root)}
		d.Free, d.Total = diskFree(ptr)
		out = append(out, d)
	}
	return out
}

func volumeLabel(rootPtr *uint16, fallback string) string {
	var name [syscall.MAX_PATH + 1]uint16
	r, _, _ := procGetVolumeInfo.Call(
		uintptr(unsafe.Pointer(rootPtr)),
		uintptr(unsafe.Pointer(&name[0])), uintptr(len(name)),
		0, 0, 0, 0, 0,
	)
	if r == 0 {
		return fallback
	}
	if label := syscall.UTF16ToString(name[:]); label != "" {
		return label + " (" + fallback + ")"
	}
	return fallback
}

func diskFree(rootPtr *uint16) (free, total uint64) {
	var avail, totalBytes, totalFree uint64
	r, _, _ := procGetDiskFree.Call(
		uintptr(unsafe.Pointer(rootPtr)),
		uintptr(unsafe.Pointer(&avail)),
		uintptr(unsafe.Pointer(&totalBytes)),
		uintptr(unsafe.Pointer(&totalFree)),
	)
	if r == 0 {
		return 0, 0
	}
	return avail, totalBytes
}
