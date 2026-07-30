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

// RemoveArchive stops a snapshot schedule from ever running again.
//
// THE ZIPS IT ALREADY WROTE ARE LEFT EXACTLY WHERE THEY ARE. Same promise as
// removing a folder or a destination: this is a change of intent, not a
// deletion of backups. Those snapshots stay readable with the password that
// made them — which is why the password is only forgotten once the job is,
// and not a moment before.
func RemoveArchive(name string) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	var kept []config.Archive
	found := false
	for _, a := range cfg.Archives {
		if a.Name == name {
			found = true
			continue
		}
		kept = append(kept, a)
	}
	if !found {
		return fmt.Errorf("no snapshot schedule named %q", name)
	}
	cfg.Archives = kept
	if err := cfg.Save(); err != nil {
		return err
	}
	// Only after the config change is durable, so a failed save never leaves a
	// live job separated from the password it cannot run without.
	state, err := config.LoadState()
	if err != nil {
		return err
	}
	delete(state.ArchivePasswords, name)
	delete(state.ArchiveLastRun, name)
	// And out of the OS keyring if the passwords are kept there, so the two
	// storages forget the same thing: the line above has always dropped the
	// password on removal, and leaving a keyring copy behind would mean whether an
	// old zip can still be opened depended on which storage was in use.
	// Best-effort for the reason RemoveTarget gives — the schedule is gone either
	// way, and a leftover entry is untidy rather than harmful.
	if state.SecretsInKeyring {
		_ = config.KeyringForget(config.ArchiveKeyringAccount(name))
	}
	return state.Save()
}

// SetArchivePaused stops or resumes a schedule without touching anything else.
func SetArchivePaused(name string, paused bool) error {
	return editArchive(name, func(a *config.Archive) error {
		a.Paused = paused
		return nil
	})
}

// SetArchiveSchedule changes how often a snapshot runs, how many are kept, and
// whether it packs everything or skips the usual junk.
//
// Reducing Keep does not delete anything here: the prune happens on the next
// run, by the same code that has always done it, so a number typed by mistake
// is a number you can correct before it costs you a snapshot.
//
// WHY noDefaultIgnores IS A POINTER. It is the one field here that is a bool, and
// "leave it alone" has to be distinguishable from "set it to false" — otherwise
// every edit of the interval would silently switch a job that was packing
// everything back to skipping node_modules.
//
// It was not editable at all until now, and that was the sharpest example of the
// cost of asking these questions through browser prompt() boxes: a prompt can ask
// for one line of text, so the wizard's third question simply had nowhere to live
// after setup. On one real machine the setting was the difference between a 4.3GB
// nightly zip and a 25GB one, and the only route to it was deleting the schedule
// and retyping a password that by design cannot be recovered.
func SetArchiveSchedule(name, every string, keep int, noDefaultIgnores *bool) error {
	return editArchive(name, func(a *config.Archive) error {
		if every != "" {
			if _, err := config.ParseEvery(every); err != nil {
				return err
			}
			a.Every = every
		}
		if keep > 0 {
			a.Keep = keep
		}
		if noDefaultIgnores != nil {
			a.NoDefaultIgnores = *noDefaultIgnores
		}
		return nil
	})
}

// editArchive applies a change to one schedule and saves, validating the whole
// config afterwards so a bad edit is refused rather than written.
func editArchive(name string, apply func(*config.Archive) error) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	idx := -1
	for i := range cfg.Archives {
		if cfg.Archives[i].Name == name {
			idx = i
			break
		}
	}
	if idx < 0 {
		return fmt.Errorf("no snapshot schedule named %q", name)
	}
	if err := apply(&cfg.Archives[idx]); err != nil {
		return err
	}
	if err := cfg.Validate(); err != nil {
		return err
	}
	return cfg.Save()
}
