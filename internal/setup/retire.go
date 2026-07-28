// SPDX-License-Identifier: MIT

package setup

import (
	"fmt"

	"github.com/phil9922/backup-maker/internal/config"
)

// ReenableResult describes what turning a stopped folder back on actually did,
// so the answer can be a sentence rather than "ok".
type ReenableResult struct {
	Folder config.Folder
	// Relinked names the destinations this folder was explicitly re-attached to.
	Relinked []string
	// Covered names destinations that back up every folder, and therefore pick
	// this one up again without being touched. Reported separately because
	// "reconnected to nas" and "nas backs up everything anyway" are different
	// facts and only one of them was an action.
	Covered []string
	// Missing names destinations that held a copy and no longer exist here.
	Missing []string
	// Archives names the snapshot jobs this folder was put back into.
	Archives []string
}

// ReenableFolder puts a stopped folder back into service.
//
// NOTHING IS RE-COPIED. The destination layout is keyed by machine name and
// label (config.DestRoot), so restoring the recorded label puts the mirror back
// over the copy already there, and needsCopy compares size and mtime — the
// first pass reconciles and writes nothing. That is the whole reason the label
// and the machine name are recorded rather than derived again here.
func ReenableFolder(id string) (ReenableResult, error) {
	cfg, err := config.Load()
	if err != nil {
		return ReenableResult{}, err
	}
	rec, ok := cfg.RetiredByID(id)
	if !ok {
		return ReenableResult{}, fmt.Errorf("no stopped folder with id %q", id)
	}
	// The same predicate the dashboard used to decide whether to draw the
	// button, so the page and the daemon cannot disagree about what is allowed.
	if why := cfg.ReenableBlockedReason(rec); why != "" {
		return ReenableResult{}, fmt.Errorf("%s", why)
	}

	// THE SAME ID, VERBATIM. A paired machine holds this folder under it, and
	// the archive jobs and sync bookkeeping key on it too; a fresh one would
	// stand up a second folder over there and retransfer everything.
	f := config.Folder{
		ID:               rec.ID,
		Path:             rec.Path,
		Label:            rec.Label,
		ExtraIgnore:      rec.ExtraIgnore,
		NoDefaultIgnores: rec.NoDefaultIgnores,
	}
	res := ReenableResult{Folder: f}
	cfg.Folders = append(cfg.Folders, f)

	byName := map[string]int{}
	for i, t := range cfg.Targets {
		byName[t.Name] = i
	}
	for _, cp := range rec.Copies {
		i, exists := byName[cp.Target]
		if !exists {
			// Restored anyway, and named. Refusing would strand somebody with a
			// record they cannot act on; inventing the destination is worse.
			res.Missing = append(res.Missing, cp.Target)
			continue
		}
		// ONLY WHAT WAS EXPLICIT. A destination whose folder list is empty
		// already means "every folder", so appending an id would convert it to
		// an explicitly scoped one and silently cut off every folder added
		// since this one was stopped.
		if !cp.Explicit {
			res.Covered = append(res.Covered, cp.Target)
			continue
		}
		cfg.Targets[i].Folders = appendMissing(cfg.Targets[i].Folders, rec.ID)
		res.Relinked = append(res.Relinked, cp.Target)
	}

	archiveByName := map[string]int{}
	for i, a := range cfg.Archives {
		archiveByName[a.Name] = i
	}
	for _, ra := range rec.Archives {
		i, exists := archiveByName[ra.Name]
		if !exists || !ra.Explicit {
			continue
		}
		cfg.Archives[i].Folders = appendMissing(cfg.Archives[i].Folders, rec.ID)
		res.Archives = append(res.Archives, ra.Name)
	}

	cfg.Retired = withoutRetired(cfg.Retired, id)
	if err := cfg.Save(); err != nil {
		return ReenableResult{}, err
	}
	return res, nil
}

// ForgetRetired drops the record and touches no files.
//
// The same promise every other removal on this API makes: the copies stay
// exactly where they are. This is for somebody who knows the backup is there
// and does not want the dashboard reminding them — or whose record has been
// blocked from re-enabling and simply wants the row gone.
func ForgetRetired(id string) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	if _, ok := cfg.RetiredByID(id); !ok {
		return fmt.Errorf("no stopped folder with id %q", id)
	}
	cfg.Retired = withoutRetired(cfg.Retired, id)
	return cfg.Save()
}

func withoutRetired(list []config.Retired, id string) []config.Retired {
	out := make([]config.Retired, 0, len(list))
	for _, r := range list {
		if r.ID != id {
			out = append(out, r)
		}
	}
	return out
}

func appendMissing(list []string, s string) []string {
	for _, v := range list {
		if v == s {
			return list
		}
	}
	return append(list, s)
}
