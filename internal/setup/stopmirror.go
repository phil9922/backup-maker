// SPDX-License-Identifier: MIT

package setup

import (
	"fmt"

	"github.com/phil9922/backup-maker/internal/config"
)

// StopMirroring stops the continuous copy of a folder and leaves its scheduled
// snapshots running.
//
// WHY THIS EXISTS SEPARATELY FROM RemoveFolder. A folder can carry both kinds
// of backup, and until now the only button was "Stop", which retires the
// folder — taking the schedule with it. Somebody who asked for a daily
// snapshot, got an unwanted continuous mirror as well, and pressed the only
// control offered, silently lost the daily snapshot they actually wanted. The
// two promises are separate, so stopping one has to be separate.
//
// It deletes nothing. Like every other stop in this program, the copies
// already written stay exactly where they are.
func StopMirroring(id string) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	idx := -1
	for i, f := range cfg.Folders {
		if f.ID == id {
			idx = i
			break
		}
	}
	if idx < 0 {
		return fmt.Errorf("no folder with id %q", id)
	}
	// Without a schedule this would leave the folder protected by nothing at
	// all while still listed as a folder — the silent no-backup state that
	// "No longer protected" exists to make visible. Stopping everything is
	// RemoveFolder's job, and it records the copies it leaves behind.
	if !cfg.Snapshotted(id) {
		return fmt.Errorf("%q has no snapshot schedule, so stopping its mirror would leave it backed up by nothing; stop the folder instead", cfg.Folders[idx].Label)
	}
	if !cfg.Mirrored(id) {
		return nil // already not mirrored; nothing to do
	}

	cfg.Folders[idx].SnapshotOnly = true

	// SnapshotOnly alone settles every destination that mirrors "every
	// folder". A destination told explicitly to mirror THIS folder is a
	// separate instruction and has to be withdrawn separately.
	for i := range cfg.Targets {
		t := &cfg.Targets[i]
		if len(t.Folders) == 0 {
			continue // covered by SnapshotOnly
		}
		kept := make([]string, 0, len(t.Folders))
		for _, fid := range t.Folders {
			if fid != id {
				kept = append(kept, fid)
			}
		}
		if len(kept) == len(t.Folders) {
			continue // this destination never named the folder
		}
		if len(kept) == 0 {
			// NEVER LET A SCOPED LIST BECOME EMPTY. An empty list means EVERY
			// folder, so writing it back here would take a destination that
			// mirrored one folder and hand it all of them. This destination
			// mirrored only the folder being stopped, so what it should do now
			// is mirror nothing — which is precisely ArchivesOnly, and
			// FoldersForTarget checks that before it ever reads the list.
			t.ArchivesOnly = true
			continue
		}
		t.Folders = kept
	}
	return cfg.Save()
}
