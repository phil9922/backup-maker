// SPDX-License-Identifier: MIT

//go:build linux || darwin

package browse

import (
	"os"
	"path/filepath"
	"syscall"
)

// Drives lists mounted volumes that could plausibly hold backups: removable
// media and explicitly mounted disks, not system directories. Anything not
// found here can still be typed in by hand.
func Drives() []Drive {
	var out []Drive
	seen := map[string]bool{}
	for _, dir := range mountParents() {
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, e := range entries {
			p := filepath.Join(dir, e.Name())
			fi, err := os.Stat(p)
			if err != nil || !fi.IsDir() {
				continue
			}
			// A mount parent with nothing mounted under it lists empty dirs;
			// requiring writability also filters read-only system mounts.
			if seen[p] || !writable(p) {
				continue
			}
			seen[p] = true
			d := Drive{Path: p, Label: e.Name()}
			d.Free, d.Total = usage(p)
			out = append(out, d)
		}
	}
	return out
}

func writable(path string) bool {
	return syscall.Access(path, 2 /* W_OK */) == nil
}

func usage(path string) (free, total uint64) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return 0, 0
	}
	bs := uint64(st.Bsize)
	return st.Bavail * bs, st.Blocks * bs
}
