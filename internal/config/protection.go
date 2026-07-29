// SPDX-License-Identifier: MIT

package config

// Mirrored reports whether any destination keeps a live continuous copy of this
// folder.
func (c *Config) Mirrored(id string) bool {
	for _, t := range c.Targets {
		for _, f := range c.FoldersForTarget(t) {
			if f.ID == id {
				return true
			}
		}
	}
	return false
}

// Snapshotted reports whether any schedule seals this folder into a zip.
func (c *Config) Snapshotted(id string) bool {
	for _, a := range c.Archives {
		for _, f := range c.FoldersForArchive(a) {
			if f.ID == id {
				return true
			}
		}
	}
	return false
}

// Protected reports whether ANYTHING is backing this folder up right now.
//
// THE QUESTION THE CONFIG COULD NOT ANSWER. A folder can be listed, watched and
// shown on the dashboard while being copied by nothing at all. Mark it
// SnapshotOnly and it is kept out of every unscoped destination — which is the
// point, and protects it from an unwanted mirror. Then delete its last schedule
// and there is nothing left: the flag that was keeping it out of the mirrors is
// now the only thing standing between it and any backup at all. Deleting a
// schedule must not delete anything else, so the folder record correctly stays,
// and the result is a folder that looks protected and is not.
//
// That is the state Desktop was found in on 2026-07-28, with the wizard still
// offering it under "a folder you're already protecting" — inviting a SECOND
// kind of backup to be added to a first one that did not exist.
//
// Built on FoldersForTarget and FoldersForArchive rather than re-deriving their
// rules, so this answer and the engines that do the copying cannot disagree.
// READ-ONLY: nothing here may change SnapshotOnly. Clearing that flag to make a
// folder "protected" again would hand it to every unscoped destination, which is
// exactly the incident CLAUDE.md records for 2026-07-28.
func (c *Config) Protected(id string) bool {
	return c.Mirrored(id) || c.Snapshotted(id)
}

// UnprotectedFolders returns the configured folders that nothing backs up, in
// config order.
func (c *Config) UnprotectedFolders() []Folder {
	var out []Folder
	for _, f := range c.Folders {
		if !c.Protected(f.ID) {
			out = append(out, f)
		}
	}
	return out
}
