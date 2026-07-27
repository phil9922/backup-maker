// SPDX-License-Identifier: MIT

package daemon

import (
	"fmt"
	"strings"
	"time"

	"github.com/phil9922/backup-maker/internal/config"
	"github.com/phil9922/backup-maker/internal/status"
	"github.com/phil9922/backup-maker/internal/webui"
)

// lanDeviceSeen records a browser arriving at the network view and reports
// whether it may read anything.
//
// Called from the LAN listener's goroutine on every request, so it takes d.mu
// only for the map work — no I/O of any kind happens under it. Writes are
// persisted through the tally's existing flush rather than saving state.json on
// every page load, which a phone left open would otherwise do every few seconds.
func (d *daemon) lanDeviceSeen(token, addr, agent string) (approved bool, code string, issued string) {
	now := time.Now()
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.state.LANDevices == nil {
		d.state.LANDevices = map[string]*config.LANDevice{}
	}
	if dev, ok := d.state.LANDevices[token]; ok && token != "" {
		dev.LastSeen = now
		dev.Addr = addr
		dev.Agent = agent
		d.tally.touch()
		return dev.Approved, dev.Code, ""
	}
	// A device we have never seen. It is recorded as PENDING and told nothing:
	// the holding page is the only thing it gets until somebody at the machine
	// decides otherwise.
	//
	// FIRST, A CAP. This runs on an unauthenticated network listener, and a
	// client that keeps no cookies — a scanner, a crawler, a curl loop — is a
	// brand new device on every single request. Without a bound, anything on
	// the wifi could grow state.json until the disk complained, from outside.
	// Oldest pending entries go first; approved devices are never evicted,
	// because those represent a decision somebody actually made.
	d.evictPending(maxPendingLANDevices - 1)
	issued = webui.NewLANDeviceToken()
	if issued == "" {
		return false, "", "" // no randomness: refuse rather than admit
	}
	code = webui.NewLANDeviceCode()
	d.state.LANDevices[issued] = &config.LANDevice{
		Code: code, FirstSeen: now, LastSeen: now, Addr: addr, Agent: agent,
	}
	d.tally.touch()
	d.log.Info("a device asked to see backup status on the network view",
		"code", code, "from", addr, "kind", agent)
	d.alerts.lanDeviceWaiting(code, agent)
	return false, code, issued
}

// maxPendingLANDevices bounds the unapproved list. Generous for a household,
// and small enough that a machine on the LAN cannot use this to fill a disk.
const maxPendingLANDevices = 20

// evictPending drops the oldest unapproved devices until at most keep remain.
// Called with d.mu held.
func (d *daemon) evictPending(keep int) {
	type entry struct {
		token string
		at    time.Time
	}
	var pending []entry
	for token, dev := range d.state.LANDevices {
		if !dev.Approved {
			pending = append(pending, entry{token, dev.FirstSeen})
		}
	}
	if len(pending) <= keep {
		return
	}
	for i := 1; i < len(pending); i++ {
		for j := i; j > 0 && pending[j].at.Before(pending[j-1].at); j-- {
			pending[j], pending[j-1] = pending[j-1], pending[j]
		}
	}
	for _, e := range pending[:len(pending)-keep] {
		delete(d.state.LANDevices, e.token)
	}
}

// approveLANDevice admits a waiting device. Keyed by the short code shown on
// both screens rather than by the token, so nothing that identifies a browser
// has to travel back through the dashboard to approve it.
func (d *daemon) approveLANDevice(code string) error {
	return d.updateLANDevice(code, func(dev *config.LANDevice) { dev.Approved = true })
}

// forgetLANDevice removes a device. On an approved one this is a revocation:
// its token stops matching and it is back to the holding page immediately.
func (d *daemon) forgetLANDevice(code string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	for token, dev := range d.state.LANDevices {
		if strings.EqualFold(dev.Code, code) {
			delete(d.state.LANDevices, token)
			d.tally.touch()
			return d.state.Save()
		}
	}
	return fmt.Errorf("no device with code %q", code)
}

func (d *daemon) updateLANDevice(code string, apply func(*config.LANDevice)) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	for _, dev := range d.state.LANDevices {
		if strings.EqualFold(dev.Code, code) {
			apply(dev)
			d.tally.touch()
			return d.state.Save()
		}
	}
	return fmt.Errorf("no device with code %q", code)
}

// lanDevices is what the settings panel renders: who is waiting, and who has
// been let in. Sorted oldest first so a queue reads as a queue.
func (d *daemon) lanDevices() []status.LANDeviceInfo {
	d.mu.Lock()
	defer d.mu.Unlock()
	out := make([]status.LANDeviceInfo, 0, len(d.state.LANDevices))
	for _, dev := range d.state.LANDevices {
		out = append(out, status.LANDeviceInfo{
			Code: dev.Code, Approved: dev.Approved, Addr: dev.Addr,
			Kind: dev.Agent, FirstSeen: dev.FirstSeen, LastSeen: dev.LastSeen,
		})
	}
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j].FirstSeen.Before(out[j-1].FirstSeen); j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}

// lanViewApprovedOnly reports whether the gate is armed, read from the live
// config so switching the setting takes effect on the next request rather than
// on the next restart.
func (d *daemon) lanViewApprovedOnly() bool {
	cfg := d.currentCfg()
	return cfg != nil && cfg.General.LANView && cfg.General.LANViewApprovedOnly()
}
