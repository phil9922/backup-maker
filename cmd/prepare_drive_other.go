// SPDX-License-Identifier: MIT

//go:build !linux

package cmd

import (
	"fmt"
	"io"
	"runtime"
)

// prepareDrive is Linux-only. macOS and Windows both mount any filesystem they
// recognise by themselves, so a drive that needs setting up needs it for a
// reason their own tools explain better than this one could — and neither has
// an /etc/fstab to record the result in.
func prepareDrive(o prepareOpts, out io.Writer) error {
	return fmt.Errorf("%s", unsupported())
}

func installSudoers(out io.Writer) error {
	return fmt.Errorf("%s", unsupported())
}

func unsupported() string {
	switch runtime.GOOS {
	case "darwin":
		return "backup-maker does not format drives on macOS. Open Disk Utility, erase the drive as APFS or Mac OS Extended, and it will appear under /Volumes — the dashboard will then offer it"
	case "windows":
		return "backup-maker does not format drives on Windows. Open Disk Management (diskmgmt.msc), initialise and format the disk, and once it has a drive letter the dashboard will offer it"
	}
	return "preparing a drive is only supported on Linux"
}
