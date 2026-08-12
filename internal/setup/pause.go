// SPDX-License-Identifier: MIT

package setup

import (
	"fmt"

	"github.com/phil9922/backup-maker/internal/config"
)

// SetMirrorPaused stops or resumes ONE folder's continuous copy to ONE
// destination, leaving that folder's other destinations running.
//
// NOTHING IS COPIED AND NOTHING IS DELETED. This is a change of intent, exactly
// like removing a folder or a destination: every file already on that
// destination stays where it is, the pair keeps its last-synced clock, and
// resuming picks the mirror up where it left off rather than starting again.
// The engine for the pair is simply not started on the next config apply.
//
// THE PAUSE IS RECORDED ON THE FOLDER, as a destination NAME in
// Folder.PausedTargets, and never as an absence from Target.Folders. Taking the
// folder out of the destination's list would pause the mirror too — and would
// empty that list for a destination scoped to one folder, which means EVERY
// folder and would hand it the whole machine. See the comment on
// Folder.PausedTargets.
func SetMirrorPaused(folderID, target string, paused bool) error {
	if folderID == "" || target == "" {
		return fmt.Errorf("say which folder and which destination to pause")
	}
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	idx := -1
	for i, f := range cfg.Folders {
		if f.ID == folderID {
			idx = i
			break
		}
	}
	if idx < 0 {
		return fmt.Errorf("no folder with id %q", folderID)
	}
	if paused {
		// Only a pair that actually exists may be paused. Pausing a mirror that
		// is not running would put a name in the config that nothing ever reads
		// again, and would answer "done" to somebody who had just asked to stop a
		// backup that was never happening.
		if !mirrors(cfg, cfg.Folders[idx].ID, target) {
			return fmt.Errorf("nothing copies %s to %q continuously, so there is no mirror there to pause",
				folderLabel(cfg.Folders[idx]), target)
		}
		if cfg.MirrorPaused(folderID, target) {
			return nil
		}
		cfg.Folders[idx].PausedTargets = append(cfg.Folders[idx].PausedTargets, target)
	} else {
		// Resuming never checks that the pair exists: a destination that has been
		// removed or renamed since must still be clearable, and the worst a
		// stale name can do here is stop nothing.
		//
		// AN EMPTY LIST IS THE CORRECT RESULT and means nothing is paused — the
		// opposite of Target.Folders, where emptying a list widens a job to every
		// folder. Nothing below may be copied to that one.
		cfg.Folders[idx].PausedTargets = withoutString(cfg.Folders[idx].PausedTargets, target)
		if len(cfg.Folders[idx].PausedTargets) == 0 {
			cfg.Folders[idx].PausedTargets = nil // keep the key out of config.toml
		}
	}
	return cfg.Save()
}

// mirrors reports whether a destination keeps a live copy of this folder,
// resolved through the same choke point the mirror engines resolve through — so
// what may be paused and what is actually running cannot drift apart.
func mirrors(cfg *config.Config, folderID, target string) bool {
	for _, t := range cfg.Targets {
		if t.Name != target {
			continue
		}
		for _, f := range cfg.FoldersForTarget(t) {
			if f.ID == folderID {
				return true
			}
		}
	}
	return false
}

// folderLabel is what to call a folder in a sentence, never an empty gap.
func folderLabel(f config.Folder) string {
	if f.Label != "" {
		return f.Label
	}
	return f.ID
}
