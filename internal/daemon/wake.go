// SPDX-License-Identifier: MIT

package daemon

import (
	"context"
	"fmt"
	"time"

	"github.com/phil9922/backup-maker/internal/status"
)

// wakeCheckEvery is how often offline targets are reconsidered for a wake.
// The per-target rate limit (wol.DefaultMinInterval) is what actually bounds
// packet frequency; this only decides how promptly we react to a target
// dropping off.
const wakeCheckEvery = time.Minute

// wakeLoop broadcasts Wake-on-LAN packets to targets that have a MAC
// configured and are currently unreachable.
//
// It is driven by the status model rather than hooked into the mirror engines
// so that all wakeable target types are covered by one code path: "share"
// targets (an SMB drive on a sleeping PC or NAS) report offline via the local
// mirror engines, and "device" targets (a paired machine) report offline via
// syncthing's cluster view.
//
// Waking is strictly best-effort. A packet leaving this machine says nothing
// about whether the target woke up; the existing offline poll is still what
// detects the target's return and triggers catch-up.
func (d *daemon) wakeLoop(ctx context.Context, collect func() status.Model) {
	tick := time.NewTicker(wakeCheckEvery)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			d.wakeOfflineTargets(collect(), time.Now())
		}
	}
}

// wakeOfflineTargets sends a magic packet for every wake-enabled target that
// looks unreachable in m. Split out from wakeLoop so it can be tested without
// a running daemon.
func (d *daemon) wakeOfflineTargets(m status.Model, now time.Time) {
	cfg := d.currentCfg()
	if cfg == nil {
		return
	}

	offline := map[string]bool{}
	for _, row := range m.Rows {
		// "stale" is offline that has lasted past the stale threshold, so it
		// wants waking too. "wrong-drive" and "error" are emphatically not
		// connectivity problems and must not trigger a wake.
		switch row.State {
		case "offline", "stale":
			offline[row.TargetName] = true
		}
	}

	for _, t := range cfg.Targets {
		if !t.WakeEnabled() || !offline[t.Name] {
			continue
		}
		if _, err := d.waker.Wake(t.Name, t.MAC, t.WakeBroadcast, now); err != nil {
			// Already logged by the waker; a bad MAC would have been caught
			// by config validation, so this is a transient network fault.
			continue
		}
	}
}

// WakeNow sends a magic packet for one target immediately, bypassing the rate
// limit. Used by the `wake` command and the dashboard button.
func (d *daemon) WakeNow(name string) error {
	cfg := d.currentCfg()
	for _, t := range cfg.Targets {
		if t.Name != name {
			continue
		}
		if t.Type == "drive" {
			return fmt.Errorf("target %q is a drive attached to this computer; there is nothing to wake", name)
		}
		if t.MAC == "" {
			return fmt.Errorf("target %q has no MAC address set; add one with: backup-maker set-mac %s <mac>", name, name)
		}
		d.waker.Forget(name)
		_, err := d.waker.Wake(t.Name, t.MAC, t.WakeBroadcast, time.Now())
		return err
	}
	return fmt.Errorf("no target named %q", name)
}
