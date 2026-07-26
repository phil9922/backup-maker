// SPDX-License-Identifier: MIT

package daemon

import (
	"errors"
	"fmt"
	"strings"

	"github.com/phil9922/backup-maker/internal/config"
	"github.com/phil9922/backup-maker/internal/pairing"
	"github.com/phil9922/backup-maker/internal/syncthing"
)

// deviceID returns this machine's own device ID, for the dashboard's QR code.
// It is only known once the (lazy) sync engine is running — a machine with no
// machine sync configured has no identity to show.
func (d *daemon) deviceID() (string, error) {
	c := d.engineClient()
	if c == nil {
		return "", errors.New("the machine-sync engine isn't running yet")
	}
	return c.MyID()
}

// acceptPair approves a machine that is waiting to back up here, from the
// dashboard's Approve button.
//
// Only a device that is ACTUALLY waiting can be approved. Approving is what
// lets another machine write into this one's receive folder, so it has to be a
// confirmation of something already asked for — never a way to add an arbitrary
// ID to the approved list.
//
// The saved config is picked up by the config watcher within a couple of
// seconds; the folders it offered are taken here and now, so clicking Approve
// visibly does something instead of appearing to hang.
func (d *daemon) acceptPair(deviceID string) error {
	c := d.engineClient()
	if c == nil {
		return errors.New("the machine-sync engine isn't running, so nothing can be waiting to pair yet")
	}
	id := strings.ToUpper(strings.TrimSpace(deviceID))
	pend, err := pairing.PendingSources(c, d.currentCfg())
	if err != nil {
		return err
	}
	waiting := false
	for _, p := range pend {
		if p.DeviceID == id {
			waiting = true
			break
		}
	}
	if !waiting {
		return fmt.Errorf("no machine with that ID is waiting for approval")
	}

	if _, err := pairing.AcceptSource(id); err != nil {
		return err
	}
	// Re-read rather than using the cached config: the copy the daemon is
	// holding predates the approval that was just written.
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	// Reconcile first: it is what introduces the approved machine to the engine,
	// and the folders it offered can't be taken before that. Both steps are
	// best-effort — the config watcher reruns them within seconds — so a hiccup
	// here delays the first copy rather than losing the approval.
	if err := syncthing.Reconcile(c, cfg, d.log); err != nil {
		d.log.Warn("engine reconcile after approval", "err", err)
		return nil
	}
	if err := pairing.ProcessPendingFolders(c, cfg, d.log); err != nil {
		d.log.Warn("processing pending folders after approval", "err", err)
	}
	return nil
}
