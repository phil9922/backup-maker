// SPDX-License-Identifier: MIT

//go:build darwin

package browse

// ListUnusable reports storage that is present but cannot hold backups yet.
//
// macOS mounts what it can under /Volumes by itself, so the case that drove
// this — a disk plugged in and left unmounted with no way to find out — does
// not arise here. What can still happen is a mount point left behind with
// nothing on it, so that much is reported.
func ListUnusable() []Unusable {
	return unmountedParents()
}
