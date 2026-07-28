// SPDX-License-Identifier: MIT

package daemon

import (
	"fmt"
	"strings"

	"github.com/phil9922/backup-maker/internal/config"
	"github.com/phil9922/backup-maker/internal/localmirror"
	"github.com/phil9922/backup-maker/internal/setup"
)

// reenableFolder puts a stopped folder back into service and reports, in a
// sentence, what that actually reconnected.
func (d *daemon) reenableFolder(id string) (any, error) {
	d.retireMu.Lock()
	defer d.retireMu.Unlock()

	res, err := setup.ReenableFolder(id)
	if err != nil {
		return nil, err
	}
	d.log.Info("a stopped folder was turned back on", "folder", res.Folder.Label, "path", res.Folder.Path)

	parts := []string{fmt.Sprintf("%s is being backed up again", res.Folder.Label)}
	if named := append(append([]string{}, res.Relinked...), res.Covered...); len(named) > 0 {
		parts = append(parts, "reconnected to "+humanList(named))
	}
	if len(res.Missing) > 0 {
		// Named rather than glossed over: the folder is protected again, but
		// not everywhere it used to be, and only saying the first half would
		// leave somebody believing in a copy that nothing is maintaining.
		parts = append(parts, humanList(res.Missing)+" no longer exists here, so nothing was linked to it")
	}
	msg := strings.Join(parts, "; ") +
		". Nothing is copied again from scratch — the backup already there is checked over and carries on."
	return map[string]any{"ok": true, "message": msg}, nil
}

// deleteRetiredBackups removes a stopped folder's copies from the destinations
// that hold them.
//
// The opener is the daemon's own backend builder, so this uses exactly the same
// connection logic — and the same stored share credentials — as the mirror
// engines do. setup owns the decision about WHAT is deleted; this owns only how
// a destination is opened.
func (d *daemon) deleteRetiredBackups(id, confirm string) (any, error) {
	d.retireMu.Lock()
	defer d.retireMu.Unlock()

	open := func(t config.Target) (localmirror.Backend, error) {
		b, _, _, err := d.buildBackend(t, d.shareCredentials())
		return b, err
	}
	res, err := setup.DeleteRetiredBackups(id, confirm, open)
	if err != nil {
		return nil, err
	}

	var removed, failed, skipped []string
	for _, o := range res.Outcomes {
		switch {
		case o.Removed:
			removed = append(removed, o.Target)
			// Deleting somebody's backups is never silent, the same rule the
			// space reclaimer follows.
			d.log.Info("deleted a stopped folder's backups", "folder", res.Label, "target", o.Target)
		case o.Skipped:
			skipped = append(skipped, o.Target)
		default:
			failed = append(failed, o.Target+" ("+o.Reason+")")
			d.log.Warn("could not delete a stopped folder's backups", "folder", res.Label, "target", o.Target, "reason", o.Reason)
		}
	}

	var parts []string
	if len(removed) > 0 {
		parts = append(parts, "Deleted from "+humanList(removed)+".")
	}
	if len(failed) > 0 {
		parts = append(parts, "Could not finish on "+humanList(failed)+
			" — those copies are still there, so this folder stays in \"No longer protected\" until you can.")
	}
	if len(skipped) > 0 {
		parts = append(parts, humanList(skipped)+" holds a copy on another computer; delete it on that machine.")
	}
	if len(parts) == 0 {
		parts = append(parts, "Nothing needed deleting.")
	}
	return map[string]any{
		"ok":          true,
		"deleted":     removed,
		"failed":      failed,
		"skipped":     skipped,
		"record_kept": res.RecordKept,
		"message":     strings.Join(parts, " "),
	}, nil
}

// forgetRetired drops the record without touching a single file.
func (d *daemon) forgetRetired(id string) error {
	d.retireMu.Lock()
	defer d.retireMu.Unlock()
	return setup.ForgetRetired(id)
}

// humanList renders a list the way a sentence needs it rather than the way a
// slice prints.
func humanList(items []string) string {
	switch len(items) {
	case 0:
		return ""
	case 1:
		return items[0]
	case 2:
		return items[0] + " and " + items[1]
	}
	return strings.Join(items[:len(items)-1], ", ") + " and " + items[len(items)-1]
}
