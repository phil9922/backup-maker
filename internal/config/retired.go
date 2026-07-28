// SPDX-License-Identifier: MIT

package config

import (
	"fmt"
)

// RetiredByID finds a stopped folder's record.
func (c *Config) RetiredByID(id string) (Retired, bool) {
	for _, r := range c.Retired {
		if r.ID == id {
			return r, true
		}
	}
	return Retired{}, false
}

// ReenableBlockedReason explains why a stopped folder cannot be turned back on,
// or returns "" when it can.
//
// HERE, IN config, SO THERE IS ONE ANSWER. The dashboard needs it to decide
// whether to draw a button or an explanation, and setup needs it to refuse at
// the moment of commit. Two implementations of that rule would eventually
// disagree, and the direction they would disagree in is a button that promises
// something the daemon then refuses — or, worse, one that does not appear while
// the action is in fact allowed.
func (c *Config) ReenableBlockedReason(r Retired) string {
	for _, f := range c.Folders {
		if f.Path == r.Path {
			return fmt.Sprintf("%s is being backed up again already — forget this record instead", r.Path)
		}
	}
	// A COLLISION HERE IS DATA LOSS, not untidiness. The destination subtree is
	// <machine>/<label>, so two folders resolving to the same one share a
	// directory — and each mirror pass versions away everything under its root
	// that is not in its own source. They would take turns deleting each
	// other's files into version history for as long as both existed.
	want := DestRoot(c.General.MachineName, r.Label)
	for _, f := range c.Folders {
		if DestRoot(c.General.MachineName, f.Label) == want {
			return fmt.Sprintf("this would back up into the same place as %q (%s), and the two would delete each other's files — rename one of them first",
				f.Label, f.Path)
		}
	}
	return ""
}

// DeleteBlockedReason explains why a stopped folder's backups must not be
// deleted, or returns "" when they may be.
//
// The check that matters is the same collision as above, read the other way
// round: if a LIVE folder is mirroring into the directory this record names,
// then deleting "the retired folder's backups" would delete a working backup
// that something is actively maintaining.
func (c *Config) DeleteBlockedReason(r Retired) string {
	live := map[string]Folder{}
	for _, f := range c.Folders {
		live[DestRoot(c.General.MachineName, f.Label)] = f
	}
	for _, cp := range r.Copies {
		if cp.DestPath == "" {
			continue
		}
		if f, ok := live[cp.DestPath]; ok {
			return fmt.Sprintf("%q (%s) is being backed up into that same place right now — deleting would remove a backup that is still in use",
				f.Label, f.Path)
		}
	}
	// Two stopped folders that landed in one directory: whichever is deleted
	// takes the other's copy with it, and the survivor's record would then
	// describe files that are gone. Deleting one at a time is refused rather
	// than guessed at.
	for _, other := range c.Retired {
		if other.ID == r.ID {
			continue
		}
		for _, a := range r.Copies {
			for _, b := range other.Copies {
				if a.Target == b.Target && a.DestPath != "" && a.DestPath == b.DestPath {
					return fmt.Sprintf("%q was stopped from the same place on %s — the two share one copy, so delete that one first",
						other.Label, a.Target)
				}
			}
		}
	}
	if !r.AnythingDeletable() {
		return "nothing here can be deleted from this computer"
	}
	return ""
}

// AnythingDeletable reports whether any of this record's copies is on storage
// this machine can actually reach. A copy on a paired computer lives on that
// computer's disk and is not ours to remove.
func (r Retired) AnythingDeletable() bool {
	for _, c := range r.Copies {
		if c.Deletable() && !c.Removed {
			return true
		}
	}
	return false
}

// Deletable reports whether this copy can be removed from this machine.
//
// FALSE FOR A PAIRED COMPUTER, ALWAYS. That copy is a receive-only folder on
// another machine's disk; this one has no filesystem access to it, and deleting
// the local source does not propagate — nor should it, since a receive-only
// holder is deliberately not driven from here. The UI says so in words rather
// than offering a button that would lie.
func (c RetiredCopy) Deletable() bool {
	return c.Type == "drive" || c.Type == "share"
}

// SafePaths reports whether this copy's recorded paths are ones we are willing
// to hand to a recursive delete. See localmirror.SafeRelPath — config.toml is
// hand-editable, so these two strings are untrusted input.
func (c RetiredCopy) SafePaths() bool {
	return SafeRelPath(c.DestPath, 2) &&
		SafeRelPath(c.VersionsPath, 3) &&
		VersionsPathLooksRight(c.VersionsPath)
}
