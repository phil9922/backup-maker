// SPDX-License-Identifier: MIT

package setup

import (
	"fmt"
	"strings"

	"github.com/phil9922/backup-maker/internal/config"
	"github.com/phil9922/backup-maker/internal/localmirror"
)

// TargetOpener opens a destination's filesystem.
//
// A seam, like daemon.newBackend: this package owns the decision about WHAT
// gets deleted and knows nothing about SMB sessions or where credentials are
// kept. It also lets the tests aim the whole thing at temp directories, which
// is the only honest way to prove that a delete removed what it should and
// nothing else.
type TargetOpener func(config.Target) (localmirror.Backend, error)

// CopyOutcome is what happened to one destination's copy.
type CopyOutcome struct {
	Target string
	// Removed is true when both the mirror and its version history are gone.
	Removed bool
	// Skipped marks a copy this machine cannot delete at all — a paired
	// computer holds its own, on its own disk.
	Skipped bool
	Reason  string
}

// DeleteResult reports the whole attempt, destination by destination.
type DeleteResult struct {
	Label    string
	Outcomes []CopyOutcome
	// RecordKept is true when something could not be finished, so the folder
	// stays in "No longer protected" and the rest can be cleared later.
	RecordKept bool
}

// DeleteRetiredBackups deletes a stopped folder's backed-up copies.
//
// THE ONLY THING IN THIS PROGRAM THAT DELETES A BACKUP ON PURPOSE, so every
// guard below is load-bearing and none of them is decided in the browser:
//
//  1. confirm must equal the folder's label. Checked here, not in the page —
//     every other decision in this codebase is made server-side and the one
//     destructive action does not get to be the exception.
//  2. The recorded paths are validated before use. config.toml is hand-edited,
//     and these two strings are handed to RemoveAll on a destination root.
//  3. A live folder must not be mirroring into the same directory. That is the
//     one case that would delete a working backup, and it is refused outright.
//  4. The storage must be recognised as ours. A recursive delete landing on a
//     stranger's disk at the same mount point is the failure worth most.
//
// Partial failure is reported, never swallowed: a destination that was
// unplugged leaves the record in place, with the reason recorded against that
// copy, so the rest can be finished later. Dropping the record on partial
// success would recreate the invisible orphan this feature exists to end.
func DeleteRetiredBackups(id, confirm string, open TargetOpener) (DeleteResult, error) {
	cfg, err := config.Load()
	if err != nil {
		return DeleteResult{}, err
	}
	rec, ok := cfg.RetiredByID(id)
	if !ok {
		return DeleteResult{}, fmt.Errorf("no stopped folder with id %q", id)
	}
	if strings.TrimSpace(confirm) != rec.Label {
		return DeleteResult{}, fmt.Errorf("nothing was deleted: the name typed did not match %q", rec.Label)
	}
	if why := cfg.DeleteBlockedReason(rec); why != "" {
		return DeleteResult{}, fmt.Errorf("%s", why)
	}

	res := DeleteResult{Label: rec.Label}
	updated := rec
	for i, cp := range rec.Copies {
		out := deleteOneCopy(cfg, cp, open)
		res.Outcomes = append(res.Outcomes, out)
		if out.Removed {
			updated.Copies[i].Removed = true
			updated.Copies[i].Error = ""
			continue
		}
		if !out.Skipped {
			updated.Copies[i].Error = out.Reason
		}
	}

	// The record goes only when every copy this machine could ever remove is
	// gone. A copy on a paired computer never blocks it — that one is deleted
	// over there, and saying so is all this machine can honestly do.
	unfinished := false
	for _, o := range res.Outcomes {
		if !o.Removed && !o.Skipped {
			unfinished = true
		}
	}
	if unfinished {
		res.RecordKept = true
		for i := range cfg.Retired {
			if cfg.Retired[i].ID == id {
				cfg.Retired[i] = updated
			}
		}
	} else {
		cfg.Retired = withoutRetired(cfg.Retired, id)
	}
	if err := cfg.Save(); err != nil {
		return res, err
	}
	return res, nil
}

// deleteOneCopy removes one destination's mirror and version history.
func deleteOneCopy(cfg *config.Config, cp config.RetiredCopy, open TargetOpener) CopyOutcome {
	out := CopyOutcome{Target: cp.Target}
	if cp.Removed {
		out.Removed = true
		return out
	}
	if !cp.Deletable() {
		out.Skipped = true
		out.Reason = "that copy is on another computer — delete it there"
		return out
	}
	// The record is untrusted input. A hand-edited path is refused rather than
	// cleaned up and used: there is no safe way to guess what was meant.
	if !cp.SafePaths() {
		out.Reason = "this record's stored paths have been edited by hand and no longer describe a safe place to delete"
		return out
	}

	var target *config.Target
	for i := range cfg.Targets {
		if cfg.Targets[i].Name == cp.Target {
			target = &cfg.Targets[i]
			break
		}
	}
	if target == nil {
		out.Reason = "that destination is no longer set up on this computer, so its copy cannot be reached from here"
		return out
	}

	b, err := open(*target)
	if err != nil {
		out.Reason = "not connected"
		return out
	}
	defer b.Close()

	// The same rule the mirror applies before writing, applied before deleting.
	// Foreign storage at a familiar mount point is exactly how a recursive
	// delete finds somebody else's files.
	state, err := config.LoadState()
	if err != nil {
		out.Reason = err.Error()
		return out
	}
	switch localmirror.Recognize(b, state.DriveTargetUUIDs[cp.Target]) {
	case localmirror.Offline:
		out.Reason = "not connected"
		return out
	case localmirror.Foreign:
		out.Reason = "that is not the storage backup-maker knows — nothing was deleted"
		return out
	}

	if err := b.RemoveAll(cp.DestPath); err != nil {
		out.Reason = err.Error()
		return out
	}
	// The history is half the data and all of the surprise: a "deleted" backup
	// that leaves thirty days of previous file contents behind has not been
	// deleted in any sense the user meant.
	if err := b.RemoveAll(cp.VersionsPath); err != nil {
		out.Reason = err.Error()
		return out
	}
	out.Removed = true
	return out
}
