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
	// Time out anything still waiting, before deciding anything about this
	// request — so an expired record cannot be answered from.
	d.expirePending(now)
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

// pendingLANDeviceTTL is how long a request to watch backups waits before it
// lapses.
//
// A REQUEST IS SOMEBODY STANDING THERE HOLDING A PHONE. If nobody answers within
// a few minutes they have walked off, and the entry left behind is worse than
// useless: it is a code sitting in a list, indistinguishable from a live
// request, that somebody may later approve without any idea what they are
// admitting. Letting it lapse means an approval is always a decision about a
// device asking RIGHT NOW.
//
// It also closes the obvious nuisance on an unauthenticated listener: anything
// on the wifi can queue up requests, and while the cap above stops them filling
// a disk, only expiry stops them cluttering the panel indefinitely.
//
// APPROVED DEVICES NEVER EXPIRE. That is a decision somebody made, and silently
// revoking it would mean a phone that worked for weeks stops one morning with
// nothing to explain why.
//
// The holding page reloads itself every 5s, so a device still watching simply
// gets a fresh code and reappears; one that has gone quietly disappears.
const pendingLANDeviceTTL = 5 * time.Minute

// expirePending drops unapproved devices whose request has lapsed. Called with
// d.mu held.
func (d *daemon) expirePending(now time.Time) {
	for token, dev := range d.state.LANDevices {
		// LastSeen, not FirstSeen: a phone sitting on the holding page is still
		// asking, and timing it out from under a person who is watching the
		// screen would be its own small betrayal.
		if !dev.Approved && now.Sub(dev.LastSeen) > pendingLANDeviceTTL {
			delete(d.state.LANDevices, token)
			d.log.Debug("a pending network-view request expired unanswered",
				"code", dev.Code, "waited", now.Sub(dev.FirstSeen).Round(time.Second))
		}
	}
}

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
	// Also expired HERE, not only when a device connects. This is the path the
	// dashboard polls, and it is the one that matters: a lapsed request must
	// leave the screen on its own, rather than sitting there looking live until
	// something happens to touch the network listener again.
	d.expirePending(time.Now())
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
