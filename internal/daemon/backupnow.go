// SPDX-License-Identifier: MIT

package daemon

import (
	"fmt"

	"github.com/phil9922/backup-maker/internal/config"
	"github.com/phil9922/backup-maker/internal/localmirror"
)

// "Back up now" — the two kinds of backup this program runs, each triggered by
// hand from the row that describes it.
//
// NEITHER OF THESE DOES THE WORK ITSELF. A continuous mirror pass belongs to
// the engine's own goroutine and a snapshot takes tens of minutes; both are
// started and reported on, never waited for. The dashboard request has to come
// back while the copying is still going, and the row it came from is what says
// how far it got.
//
// Both are also read-only with respect to configuration: they run what is
// already set up, exactly as set up. Nothing here writes config.toml, and in
// particular nothing here constructs a Target or a folder list — a manual run
// of a schedule that covers one folder must not become a run that covers every
// folder.

// backUpFolderNow forces a full mirror pass for one folder→destination pair,
// instead of waiting for the hourly rescan.
func (d *daemon) backUpFolderNow(folderID, targetName string) (any, error) {
	if folderID == "" || targetName == "" {
		return nil, fmt.Errorf("say which folder and which destination to back up now")
	}
	// Paused first, and before the engine is looked for. A paused pair HAS no
	// engine, so without this it would be refused as a pair nothing is copying —
	// true, and useless: it does not say why, and it does not say what to press.
	// Same shape as the paused refusal on the snapshot side.
	if cfg := d.currentCfg(); cfg != nil && cfg.MirrorPaused(folderID, targetName) {
		return nil, fmt.Errorf("copying %s to %s is paused. Resume it first — a paused mirror does not run, by hand or on its own",
			d.folderLabel(folderID), targetName)
	}
	var engine *localmirror.Engine
	for _, e := range d.currentEngines() {
		if e.FolderID == folderID && e.TargetName == targetName {
			engine = e
			break
		}
	}
	label := d.folderLabel(folderID)
	if engine == nil {
		// A stopped folder, a snapshot-only destination, a paired machine (which
		// syncthing keeps up to date and this button does not drive), or simply a
		// stale page. Whatever the cause, there is no continuous mirror of this
		// pair to run.
		return nil, fmt.Errorf("nothing is copying %s to %s continuously, so there is no backup to run here",
			label, targetName)
	}

	// The refusals the engine would hit the moment it started, said now instead.
	// A pass that begins and gives up is indistinguishable from a button that
	// did nothing.
	switch state := engine.Status().State; state {
	case "offline":
		return nil, fmt.Errorf("%s is not reachable right now. backup-maker checks for it every few seconds and catches up on its own the moment it is back", targetName)
	case "wrong-drive":
		return nil, fmt.Errorf("the storage at %s is not the storage this destination was set up against, so nothing may be written to it. If you meant to replace it, set the destination up again", targetName)
	case "name-clash":
		return nil, fmt.Errorf("another computer already backs up to %s under this machine's name, so nothing may be written there. Set this destination up again in a folder of its own", targetName)
	}

	busy := engine.Busy()
	engine.BackUpNow()
	// Truthful about when: this asks for a pass, it does not perform one, and
	// the pass may take minutes on a network destination.
	if busy {
		return map[string]any{"ok": true, "message": fmt.Sprintf(
			"%s is already being checked against %s. A full check will run as soon as that finishes.", label, targetName)}, nil
	}
	return map[string]any{"ok": true, "message": fmt.Sprintf(
		"Checking %s against %s now. It takes as long as it takes — the row shows how far it has got.", label, targetName)}, nil
}

// backUpArchiveNow writes one schedule's encrypted snapshot now, instead of
// waiting for its slot.
func (d *daemon) backUpArchiveNow(name string) (any, error) {
	cfg := d.currentCfg()
	if cfg == nil {
		return nil, fmt.Errorf("no configuration loaded")
	}
	var job *config.Archive
	for i := range cfg.Archives {
		if cfg.Archives[i].Name == name {
			job = &cfg.Archives[i]
			break
		}
	}
	if job == nil {
		return nil, fmt.Errorf("no snapshot schedule called %q", name)
	}

	// The same three reasons the panel greys the button out, checked again here
	// because the panel's copy of them is a page that may be minutes old.
	if job.Paused {
		return nil, fmt.Errorf("%q is paused. Resume it first — a paused schedule does not run, by hand or on its timer", name)
	}
	if !d.hasArchivePassword(name) {
		return nil, fmt.Errorf("%q has no stored password, so it cannot write a snapshot. Enter its password first", name)
	}
	// Resolved through the choke point the snapshot writer itself uses, so what
	// is refused here and what would actually be packed cannot drift apart.
	if len(cfg.FoldersForArchive(*job)) == 0 {
		return nil, fmt.Errorf("%q covers no folder that is still being backed up, so there is nothing to put in a snapshot", name)
	}

	if !d.claimArchiveRun(name) {
		return nil, fmt.Errorf("%q is already writing a snapshot. Wait for that one to finish — the row shows how far it has got", name)
	}
	// OFF THE REQUEST GOROUTINE. This is minutes of packing, encrypting and
	// verifying; the dashboard's POST must answer now and let the row report
	// the rest. Same shape as the scheduler's own call, claim released by defer
	// so a panic cannot leave the schedule permanently claimed.
	run := *job
	go func() {
		defer d.releaseArchiveRun(name)
		d.runArchive(cfg, run)
	}()

	return map[string]any{"ok": true, "message": fmt.Sprintf(
		"Writing a snapshot for %q now. A large folder takes tens of minutes; the row shows how far it has got. "+
			"This one counts as the schedule's latest run, so the next is due a full interval from now.", name)}, nil
}

// folderLabel is what to call a folder in a sentence, falling back to its id
// where there is nothing better — never an empty gap in the middle of a message.
func (d *daemon) folderLabel(id string) string {
	if cfg := d.currentCfg(); cfg != nil {
		for _, f := range cfg.Folders {
			if f.ID == id {
				if f.Label != "" {
					return f.Label
				}
				break
			}
		}
	}
	return id
}
