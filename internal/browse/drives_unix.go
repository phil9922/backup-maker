// SPDX-License-Identifier: MIT

//go:build linux || darwin

package browse

import (
	"os"
	"path/filepath"
	"syscall"
)

// mountParentsFn is the platform's list of directories that drives are mounted
// under, indirected so tests can supply a fixture directory instead.
var mountParentsFn = mountParents

// Drives lists mounted volumes that could plausibly hold backups: removable
// media and explicitly mounted disks, not system directories. Anything not
// found here can still be typed in by hand.
func Drives() []Drive {
	var out []Drive
	seen := map[string]bool{}
	parents := mountParentsFn()
	for _, dir := range parents {
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
			// Requiring writability filters read-only system mounts. Requiring
			// that something is actually mounted there is what stops an empty
			// directory left behind by an absent drive from being offered as
			// the drive — see isMountPoint.
			if seen[p] || isParent(p, parents) || !writable(p) || !isMountPoint(p) {
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

// isParent reports whether path is itself one of the directories drives get
// mounted under. /media/<user> lives inside /media, so scanning /media finds it
// and would otherwise report the user's own mount parent as an empty drive
// bay — noise at best, and at worst it teaches people to ignore the warning
// that matters.
func isParent(path string, parents []string) bool {
	clean := filepath.Clean(path)
	for _, p := range parents {
		if filepath.Clean(p) == clean {
			return true
		}
	}
	return false
}

// isMountPoint reports whether something is actually mounted at path, by
// comparing its device number with its parent's. A mounted filesystem is a
// different device from the directory it covers; an ordinary empty folder is
// not.
//
// This matters more than it sounds. A backup destination is conventionally
// prepared as an empty directory — /mnt/backups — which the drive is then
// mounted over. If the drive fails to appear, the bare directory is still
// there, still writable, and indistinguishable from the real thing by name
// alone. Offering it would send somebody's backups to the system disk under
// the impression they were going to an external drive: it fills the boot disk,
// and the backup is lost with the machine it was meant to survive.
//
// A bind mount of a directory from the same filesystem shares its parent's
// device and so reads as "not mounted" here. That is the safe way to be wrong.
func isMountPoint(path string) bool {
	var here, up syscall.Stat_t
	if err := syscall.Stat(path, &here); err != nil {
		return false
	}
	if err := syscall.Stat(filepath.Dir(path), &up); err != nil {
		return false
	}
	return here.Dev != up.Dev
}

// unmountedParents reports the directories isMountPoint just rejected, so the
// dashboard can say why a folder that looks like a drive is not being offered
// as one.
func unmountedParents() []Unusable {
	var out []Unusable
	seen := map[string]bool{}
	parents := mountParentsFn()
	for _, dir := range parents {
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, e := range entries {
			p := filepath.Join(dir, e.Name())
			fi, err := os.Stat(p)
			if err != nil || !fi.IsDir() || seen[p] {
				continue
			}
			seen[p] = true
			if isParent(p, parents) || isMountPoint(p) {
				continue
			}
			free, total := usage(p)
			out = append(out, Unusable{
				Name:   e.Name(),
				Path:   p,
				Size:   total,
				Reason: ReasonNotAMount,
				Detail: "Nothing is mounted here. This is an ordinary folder on this computer's own disk, so backups sent to it would fill up this computer instead of an external drive. If a drive belongs here, it has not been mounted.",
				Free:   free,
			})
		}
	}
	return out
}

func usage(path string) (free, total uint64) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return 0, 0
	}
	bs := uint64(st.Bsize)
	return st.Bavail * bs, st.Blocks * bs
}
