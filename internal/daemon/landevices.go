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
func (d *daemon) lanDeviceSeen(token, addr, agent string) (approved bool, code, name, issued string) {
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
		// A denial that has run its course is lifted HERE, on a sighting, and
		// never by the clock alone. A device that was turned down and then
		// unplugged for a month must not climb back into the queue on its own
		// while nobody is asking for anything — the queue is for people
		// standing there waiting, and an entry nobody is behind is exactly the
		// stale code the five-minute expiry exists to prevent.
		//
		// The record is reused rather than replaced, so the second ask arrives
		// carrying the name the device gave the first time. "Phil's iPhone is
		// asking again" is a better question to be handed than "a device is
		// asking", and it is only knowable because the denial kept the row.
		if !dev.Approved && !dev.DeniedAt.IsZero() && now.Sub(dev.DeniedAt) > deniedLANDeviceTTL {
			dev.DeniedAt = time.Time{}
			dev.FirstSeen = now
			d.log.Info("a denied device is asking again, its denial having lapsed",
				"code", dev.Code, "name", dev.Name, "from", addr, "kind", agent)
			d.alerts.lanDeviceWaiting(dev.Code, agent, dev.Name)
		}
		d.tally.touch()
		return dev.Approved, dev.Code, dev.Name, ""
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
		return false, "", "", "" // no randomness: refuse rather than admit
	}
	code = webui.NewLANDeviceCode()
	d.state.LANDevices[issued] = &config.LANDevice{
		Code: code, FirstSeen: now, LastSeen: now, Addr: addr, Agent: agent,
	}
	d.tally.touch()
	d.log.Info("a device asked to see backup status on the network view",
		"code", code, "from", addr, "kind", agent)
	// No name yet — it is typed on the holding page this request is about to
	// serve, so the first alert can only ever describe the kind of device. The
	// name reaches the dashboard, which is where the Approve button is and
	// where the decision is actually made; raising a SECOND notification when
	// it arrives would be a nag for something already on the screen the first
	// one sent you to.
	d.alerts.lanDeviceWaiting(code, agent, "")
	return false, code, "", issued
}

// lanDeviceNamed records what a device calls itself. Called from the network
// listener's goroutine, like lanDeviceSeen, and takes d.mu for the map work
// only.
//
// Keyed by TOKEN, not by code: this is a device labelling itself, so the only
// record it may touch is the one its own cookie already identifies. Keying it
// on the code — which is on screen, and short — would let anything on the wifi
// rename somebody else's pending request by guessing at it, which is how a row
// gets approved for the wrong reason.
func (d *daemon) lanDeviceNamed(token, name string) bool {
	if token == "" {
		return false
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	dev, ok := d.state.LANDevices[token]
	if !ok {
		return false
	}
	dev.Name = config.SafeDeviceName(name)
	dev.LastSeen = time.Now()
	d.tally.touch()
	d.log.Debug("a device waiting for approval gave itself a name",
		"code", dev.Code, "name", dev.Name)
	return true
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

// deniedLANDeviceTTL is how long "no" lasts before the device may ask again.
//
// A WEEK, and the length is the whole design. Denying is a one-click answer to
// something that interrupted you, often while doing something else, and it is
// aimed at a device you may not have identified — so it must not be a
// permanent sentence on a phone that was simply asked about at a bad moment.
// What it must do is make the pestering stop NOW: a device refused and then
// re-alerting five seconds later is the behaviour that makes somebody switch
// alerting off altogether, and switched-off alerting is how a broken backup
// goes unnoticed.
//
// A week is long enough that the neighbour's phone that wandered onto the wifi
// is gone by the time it lapses, and short enough that "I denied my own tablet
// by accident" is fixed by waiting rather than by finding a setting. It can be
// fixed immediately anyway: a denied device is listed in the settings panel
// with Allow beside it.
const deniedLANDeviceTTL = 7 * 24 * time.Hour

// expirePending drops unapproved devices whose request has lapsed. Called with
// d.mu held.
func (d *daemon) expirePending(now time.Time) {
	for token, dev := range d.state.LANDevices {
		if dev.Approved {
			continue
		}
		// A DENIED DEVICE IS NOT A PENDING ONE, and running the five-minute
		// clock over it would undo the denial rather than the request: the
		// record would be dropped, the next reload would arrive with a cookie
		// matching nothing, and the device would be admitted to the queue as
		// brand new — alert and all — about three hundred seconds after being
		// turned down. The denial is kept until it lapses on its own terms.
		if !dev.DeniedAt.IsZero() {
			// Once it has been quiet for as long as the denial lasts, there is
			// nothing left to remember: the device stopped asking and the "no"
			// it was given has run out. If it ever comes back it is a stranger
			// again, which is the right footing.
			if now.Sub(dev.LastSeen) > deniedLANDeviceTTL {
				delete(d.state.LANDevices, token)
				d.log.Debug("a denied device stopped asking and has been forgotten",
					"code", dev.Code, "name", dev.Name)
			}
			continue
		}
		// LastSeen, not FirstSeen: a phone sitting on the holding page is still
		// asking, and timing it out from under a person who is watching the
		// screen would be its own small betrayal.
		if now.Sub(dev.LastSeen) > pendingLANDeviceTTL {
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
		// Denied devices are exempt for the same reason approved ones are: both
		// are answers somebody gave, and this eviction exists to bound what
		// STRANGERS can create. Letting a flood of new requests push the
		// denials out would hand anything on the wifi a way to undo them —
		// twenty page loads and the device you turned down is alerting again.
		// Denials cannot themselves be flooded: the only way to make one is for
		// a person at the machine to press Deny.
		if !dev.Approved && dev.DeniedAt.IsZero() {
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
//
// Clearing DeniedAt is what makes "Allow" work on a device turned down
// earlier: without it the device would be approved and simultaneously still
// carry a denial, and the next expiry sweep would read the pair as a lapsed
// denial to tidy away.
func (d *daemon) approveLANDevice(code string) error {
	return d.updateLANDevice(code, func(dev *config.LANDevice) {
		dev.Approved = true
		dev.DeniedAt = time.Time{}
	})
}

// denyLANDevice turns a waiting device down: it keeps reading the holding page,
// but it stops asking out loud.
//
// WHY THIS IS NOT "FORGET". Forgetting deletes the record, which deletes the
// only thing that recognises that browser — so the device's next reload, five
// seconds later, arrives with a cookie matching nothing, is filed as a device
// nobody has ever seen, and raises a fresh alert under a fresh code. Denying by
// deleting therefore produced an alert every five seconds for as long as the
// device kept the page open, and the only way out was to switch the whole
// category of notifications off. Keeping the record is what lets the answer be
// remembered and the interruption actually end.
//
// The device is told none of this. It sees the same holding page as any other
// unapproved device, because "you were refused" is both a disclosure to
// somebody deliberately not trusted and an invitation to try again from another
// browser.
func (d *daemon) denyLANDevice(code string) error {
	now := time.Now()
	return d.updateLANDevice(code, func(dev *config.LANDevice) {
		dev.Approved = false
		dev.DeniedAt = now
		d.log.Info("a device asking to see backup status was turned down",
			"code", dev.Code, "name", dev.Name, "from", dev.Addr,
			"silent_for", deniedLANDeviceTTL)
	})
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
			// The list itself is memory's to write (see carryOverFromMemory);
			// everything else in the file is left exactly as it was found.
			return d.updateState(func(*config.State) error { return nil })
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
			return d.updateState(func(*config.State) error { return nil })
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
			Code: dev.Code, Approved: dev.Approved, Denied: !dev.DeniedAt.IsZero(),
			Addr: dev.Addr, Kind: dev.Agent, Name: dev.Name,
			FirstSeen: dev.FirstSeen, LastSeen: dev.LastSeen,
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
