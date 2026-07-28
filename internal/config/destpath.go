// SPDX-License-Identifier: MIT

package config

import (
	"path"
	"strings"
)

// VersionsDirName holds displaced file versions inside a destination's root,
// mirroring syncthing's .stversions concept and naming scheme.
//
// HERE RATHER THAN IN localmirror, WHICH OWNS THE BEHAVIOUR, because the
// destination's layout is now something three packages have to agree on:
// localmirror writes it, setup deletes it when a stopped folder's backups are
// removed for good, and status reports it. localmirror already imports this
// package, so this is the one place all of them can reach. It is re-exported
// there, so nothing in the mirror engine had to change.
const VersionsDirName = ".backup-maker-versions"

// sanitize makes one path component safe for FAT, NTFS and SMB.
func sanitize(name string) string {
	out := []rune(name)
	for i, r := range out {
		switch r {
		case '<', '>', ':', '"', '/', '\\', '|', '?', '*':
			out[i] = '_'
		}
	}
	if len(out) == 0 {
		return "unnamed"
	}
	return string(out)
}

// DestRoot is where one folder's live mirror lands inside a destination's root:
// <machine>/<label>, both parts made safe for FAT, NTFS and SMB.
//
// EXPORTED SO THERE IS EXACTLY ONE OF IT. This used to be an expression inlined
// in New(), which was fine while the engine was the only thing that needed to
// know. It is not fine now: a stopped folder records this path and a later
// "delete these backups" aims a recursive removal at it. A second, subtly
// different copy of the expression is how such a delete misses the data it was
// asked to remove — or, far worse, finds somebody else's.
//
// KEYED BY LABEL, NOT FOLDER ID, and that is load-bearing in both directions.
// It means re-adding a folder under the same label reconciles against the copy
// already on the destination instead of writing a second one, which is what
// makes turning a stopped folder back on cost nothing. It also means two
// folders that share a label under one machine name share a directory and will
// version each other's files away, which is why anything restoring a folder has
// to check for that collision first.
func DestRoot(machineName, label string) string {
	return path.Join(MachineDir(machineName), sanitize(label))
}

// MachineDir is the single directory at a destination root that belongs to one
// machine: every live mirror it keeps there, its manifest, its status page and
// its claim all sit under this one name.
//
// SPLIT OUT OF DestRoot FOR THE SAME REASON DestRoot WAS SPLIT OUT OF New():
// three things now need to name that directory without naming a folder inside
// it — the claim that decides whether the directory is ours at all, the
// manifest, and the status page. A second spelling of sanitize(machineName)
// elsewhere is how one of them ends up looking at a different directory from
// the one the mirror is writing to, and the claim is the one that must not.
func MachineDir(machineName string) string { return sanitize(machineName) }

// VersionRoot is where that same folder's displaced versions accumulate.
//
// The version store mirrors the live tree's shape underneath one directory at
// the destination root (see versionPath), so a folder's history is always its
// DestRoot with the store's name in front. Deleting a folder's backups means
// removing both, and forgetting the second is how "deleted" leaves thirty days
// of the file's previous contents sitting on the disk.
func VersionRoot(machineName, label string) string {
	return path.Join(VersionsDirName, DestRoot(machineName, label))
}

// SafeRelPath reports whether p is a relative destination path this package is
// willing to hand to a recursive delete.
//
// THE RECORD IT VALIDATES IS HAND-EDITABLE. Retired folders live in
// config.toml, which this project treats as a file people open and change, and
// their stored paths are handed to RemoveAll on a destination root. An absolute
// path, a "..", or an empty string in that field turns a delete of one folder's
// backups into something else entirely — so the record is treated as untrusted
// input rather than as something we wrote and can therefore trust.
//
// minSegments guards the shape rather than the characters: a live mirror is
// always <machine>/<label>, so a one-segment path means the record has lost a
// part and would delete every folder belonging to that machine.
func SafeRelPath(p string, minSegments int) bool {
	if p == "" || path.IsAbs(p) || strings.HasPrefix(p, "/") {
		return false
	}
	// A Windows-style absolute path is not caught by path.IsAbs, which only
	// knows about slashes.
	if strings.Contains(p, `\`) || (len(p) > 1 && p[1] == ':') {
		return false
	}
	if path.Clean(p) != p {
		return false
	}
	if p == "." || p == ".." {
		return false
	}
	for _, seg := range strings.Split(p, "/") {
		if seg == "" || seg == "." || seg == ".." {
			return false
		}
	}
	return len(strings.Split(p, "/")) >= minSegments
}

// SameDest reports whether two destination paths land in the same place.
//
// CASE-INSENSITIVE, BECAUSE THE FILESYSTEMS BACKUPS LAND ON MOSTLY ARE. An SD
// card or USB stick is almost always exFAT or FAT, a Windows share is NTFS,
// and all of them treat "Development" and "development" as one directory —
// while ext4 underneath a Linux drive target treats them as two. Comparing the
// strings exactly would let a pair of labels differing only in case through the
// collision check, and on the commonest destination of all they would then
// share a directory and version each other's files away.
//
// Refusing a case-only difference costs somebody a slightly different label.
// Allowing it costs them a backup. The comparison is deliberately the stricter
// of the two readings, on every platform, so the answer does not depend on
// which destination happens to be plugged in.
func SameDest(a, b string) bool { return strings.EqualFold(a, b) }

// VersionsPathLooksRight reports whether p lives inside the version store.
//
// Checked separately from SafeRelPath because "relative and tidy" is not
// enough for the path a delete aims at the history: a record whose versions
// path had been edited to point at the live tree would delete the backup while
// claiming to be removing old versions of it.
func VersionsPathLooksRight(p string) bool {
	return strings.HasPrefix(p, VersionsDirName+"/")
}

// TargetLocation is where a destination is, in the words a person recognises:
// the mount path of a drive, the URL of a share, an abbreviated device id for
// a paired computer.
//
// A device id is truncated at its first group because the whole thing is 63
// characters of base32 and identifies the machine no better, on a line whose
// job is to let somebody tell one destination from another at a glance.
func TargetLocation(t Target) string {
	switch t.Type {
	case "drive":
		return t.Path
	case "share":
		return t.URL
	case "device":
		if i := strings.IndexByte(t.DeviceID, '-'); i > 0 {
			return t.DeviceID[:i] + "…"
		}
		return t.DeviceID
	}
	return ""
}
