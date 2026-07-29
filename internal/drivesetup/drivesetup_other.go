// SPDX-License-Identifier: MIT

//go:build !linux

package drivesetup

import (
	"context"
	"fmt"
	"runtime"
)

// Allowed is false everywhere but Linux: there is nothing here to escalate to.
func Allowed() bool { return false }

func Run(ctx context.Context, r Request) (string, error) {
	return "", fmt.Errorf("%s", advice())
}

// Manual points at the tool that owns disks on this platform, rather than
// pretending backup-maker could do it.
func Manual(r Request) []string { return []string{advice()} }

func AllowCommand() string { return "" }

func CommandPrefix() string { return "" }

func advice() string {
	if runtime.GOOS == "darwin" {
		return "Open Disk Utility, erase the drive, and it will appear under /Volumes for the dashboard to offer."
	}
	return "Open Disk Management (diskmgmt.msc), initialise and format the disk, and once it has a drive letter the dashboard will offer it."
}
