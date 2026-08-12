// SPDX-License-Identifier: MIT

package daemon

import (
	"fmt"

	"github.com/phil9922/backup-maker/internal/config"
	"github.com/phil9922/backup-maker/internal/localmirror"
	"github.com/phil9922/backup-maker/internal/setup"
)

// destOpener is the daemon's own way of opening a destination, so browsing one
// uses exactly the connection logic — and the stored share credentials — the
// mirror engines use. setup owns every decision about what may be shown or
// removed; this owns only how the storage is reached.
func (d *daemon) destOpener() setup.TargetOpener {
	creds := d.shareCredentials()
	return func(t config.Target) (localmirror.Backend, error) {
		b, _, _, err := d.buildBackend(t, creds)
		return b, err
	}
}

// destFiles lists one directory of what a destination holds. Read-only: it
// opens the destination, lists one directory and closes it again.
func (d *daemon) destFiles(target, rel string) (any, error) {
	return setup.ListDestFiles(target, rel, d.destOpener())
}

// deleteDestFile removes one thing a destination holds.
//
// No lock, deliberately: this writes no configuration, and the paths it can
// reach are the ones nothing is maintaining — anything a live folder, schedule
// or destination owns is refused by setup before a file is touched. The
// deletion itself is logged, because deleting somebody's backups is never
// silent here.
func (d *daemon) deleteDestFile(target, rel, confirm string) (any, error) {
	res, err := setup.DeleteDestFile(target, rel, confirm, d.destOpener())
	if err != nil {
		d.log.Warn("refused to delete something from a destination",
			"target", target, "path", rel, "reason", err)
		return nil, err
	}
	d.log.Info("deleted something from a destination at the owner's request",
		"target", res.Target, "path", res.Path, "kind", res.Kind, "owner", res.Owner)
	return map[string]any{
		"ok":      true,
		"deleted": res.Path,
		"message": fmt.Sprintf("Deleted %q from %s. Nothing on this computer was touched.", res.Name, res.Target),
	}, nil
}
