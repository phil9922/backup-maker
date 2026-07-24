// SPDX-License-Identifier: MIT

package setup

import (
	"fmt"

	"github.com/phil9922/backup-maker/internal/config"
)

// RemoveFolder stops backing up a folder and drops every dangling reference to
// it from targets and archive jobs.
//
// Files already written to backup targets are left exactly where they are:
// removing a folder is a change of intent, never a deletion of backups.
func RemoveFolder(id string) error {
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
	cfg.Folders = append(cfg.Folders[:idx], cfg.Folders[idx+1:]...)
	for ti := range cfg.Targets {
		cfg.Targets[ti].Folders = withoutString(cfg.Targets[ti].Folders, id)
	}
	for ai := range cfg.Archives {
		cfg.Archives[ai].Folders = withoutString(cfg.Archives[ai].Folders, id)
	}
	return cfg.Save()
}

// RemoveTarget stops backing up to a destination. Archive jobs pointed at it
// are removed too, since an archive without a destination cannot run.
//
// As with RemoveFolder, existing backup data on the target is left untouched.
func RemoveTarget(name string) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	idx := -1
	for i, t := range cfg.Targets {
		if t.Name == name {
			idx = i
			break
		}
	}
	if idx < 0 {
		return fmt.Errorf("no target named %q", name)
	}
	cfg.Targets = append(cfg.Targets[:idx], cfg.Targets[idx+1:]...)

	var keptArchives []config.Archive
	var orphaned []string
	for _, a := range cfg.Archives {
		if a.Target == name {
			orphaned = append(orphaned, a.Name)
			continue
		}
		keptArchives = append(keptArchives, a)
	}
	cfg.Archives = keptArchives

	if err := cfg.Save(); err != nil {
		return err
	}

	// Forget the secrets that belonged to this target only after the config
	// change is durable, so a failed save never orphans a live target from its
	// credentials.
	state, err := config.LoadState()
	if err != nil {
		return err
	}
	delete(state.ShareCredentials, name)
	delete(state.DriveTargetUUIDs, name)
	for _, an := range orphaned {
		delete(state.ArchivePasswords, an)
		delete(state.ArchiveLastRun, an)
	}
	return state.Save()
}

func withoutString(list []string, s string) []string {
	out := make([]string, 0, len(list))
	for _, v := range list {
		if v != s {
			out = append(out, v)
		}
	}
	return out
}
