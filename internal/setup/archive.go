// SPDX-License-Identifier: MIT

package setup

import (
	"fmt"

	"github.com/phil9922/backup-maker/internal/config"
)

// AddArchive schedules a FULL backup: a password-protected AES-256 zip snapshot
// written to a drive or network-drive target on a timer.
//
// The password is REQUIRED and is stored only in the private state.json. There
// is no recovery path: without it the archives cannot be opened.
func AddArchive(name string, folderIDs []string, every, target string, keep int, password string) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	if name == "" {
		return fmt.Errorf("a name is required")
	}
	for _, a := range cfg.Archives {
		if a.Name == name {
			return fmt.Errorf("an archive named %q already exists", name)
		}
	}
	if password == "" {
		return fmt.Errorf("a password is required for archives")
	}
	if _, err := config.ParseEvery(every); err != nil {
		return err
	}
	if len(cfg.Folders) == 0 {
		return fmt.Errorf("no folders configured yet — add a folder first")
	}

	// Only mirror-style targets can hold archives; a paired machine syncs a
	// live mirror rather than accepting arbitrary files.
	var dest *config.Target
	for i, t := range cfg.Targets {
		if t.Name == target && (t.Type == "drive" || t.Type == "share") {
			dest = &cfg.Targets[i]
			break
		}
	}
	if dest == nil {
		return fmt.Errorf("no drive or network-drive target named %q to write archives to", target)
	}

	known := map[string]bool{}
	for _, f := range cfg.Folders {
		known[f.ID] = true
	}
	for _, id := range folderIDs {
		if !known[id] {
			return fmt.Errorf("unknown folder id %q", id)
		}
	}

	if keep <= 0 {
		keep = config.DefaultArchiveKeep
	}
	cfg.Archives = append(cfg.Archives, config.Archive{
		Name: name, Folders: folderIDs, Every: every, Target: target, Keep: keep,
	})
	if err := cfg.Save(); err != nil {
		return err
	}

	state, err := config.LoadState()
	if err != nil {
		return err
	}
	if state.ArchivePasswords == nil {
		state.ArchivePasswords = map[string]string{}
	}
	state.ArchivePasswords[name] = password
	return state.Save()
}
