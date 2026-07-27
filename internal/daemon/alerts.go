// SPDX-License-Identifier: MIT

package daemon

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/phil9922/backup-maker/internal/config"
	"github.com/phil9922/backup-maker/internal/notify"
	"github.com/phil9922/backup-maker/internal/status"
)

// alert is one notification owed to the user.
type alert struct {
	urgency notify.Urgency
	title   string
	body    string
}

// alerter turns the health model into desktop notifications.
//
// TWO RULES GOVERN EVERYTHING HERE.
//
// It fires on TRANSITIONS, never on a timer. The status model is recomputed
// once a minute; announcing a broken destination on every cycle would teach the
// user to dismiss backup-maker without reading it, which is worse than never
// alerting at all. This is the same "say it once, on the way in" discipline
// recordSample and the foreign-storage refusal already follow.
//
// And it is BEST EFFORT. Delivery happens on its own goroutine with a timeout
// inside it, so a desktop that never answers cannot hold up the status loop,
// and a machine with no desktop at all — the normal state of a Raspberry Pi
// target — is not an error.
type alerter struct {
	log *slog.Logger

	mu sync.Mutex
	// notifier is the fan-out across every delivery method that is switched
	// on. Guarded, because a config apply swaps it while alerts may be in
	// flight. One sink failing never stops the others (see notify.Multi):
	// a webhook pointing at a dead host must not take desktop alerts down
	// with it.
	notifier notify.Notifier
	// on mirrors [general].desktop_alerts, updated on every config apply so
	// switching it off takes effect on a running daemon. Held here rather than
	// read back from the config, because the foreign-storage path is called
	// while the status writer holds the daemon's own lock.
	on bool
	// delivered is called with the per-method outcome of every alert, so the
	// daemon can record which delivery methods are actually working. Without
	// it a webhook that stopped working would be silent — and silent alerting
	// is the failure this whole feature exists to prevent.
	delivered func([]notify.Result)
	// kinds mirrors [general.alerts]: which categories the user still wants.
	// The zero value means every category on, which is what makes an
	// unconfigured daemon behave exactly as it did before the setting existed.
	kinds config.AlertKinds
	// prev is last cycle's health. nil means the daemon has just started (or
	// alerts were just switched on), which is what makes a problem that was
	// already there when we started get announced once.
	prev *health
}

// health is the reduced form of the status model that alerting compares between
// cycles: what would change a notification, and nothing else.
type health struct {
	// broken names the destinations the user has already been told about, and
	// failedJobs the snapshot jobs. Both are STICKY: they survive the states in
	// between and only clear when the thing actually works again. That is what
	// stops a full card being announced afresh every time it is unplugged, and
	// what makes sure the all-clear still arrives when it finally comes good.
	//
	// EVERY STICKY ALERT OWES AN ALL-CLEAR. A notification that deliberately
	// stays on screen saying backups have stopped, with nothing ever to say
	// they resumed, sends the user to check by hand — which is the errand this
	// whole feature exists to remove.
	broken     map[string]bool
	failedJobs map[string]bool
	pending    map[string]bool // device id of a machine asking to pair
}

func newAlerter(n notify.Notifier, log *slog.Logger, enabled bool) *alerter {
	return &alerter{notifier: n, log: log, on: enabled}
}

// setNotifier swaps the delivery fan-out, on start and on every config apply.
func (a *alerter) setNotifier(n notify.Notifier) {
	if a == nil {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	a.notifier = n
}

// sink reads the current fan-out under the lock, so delivery never races a
// config apply swapping it.
func (a *alerter) sink() notify.Notifier {
	if a == nil {
		return nil
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.notifier
}

// setEnabled applies [general].desktop_alerts, on start and on every reload.
func (a *alerter) setEnabled(v bool) {
	if a == nil {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	a.on = v
}

// setKinds applies [general.alerts], the per-category narrowing, on start and
// on every reload. Held here rather than read back from the config for the same
// reason `on` is: the foreign-storage path asks while the daemon's own lock is
// held elsewhere.
func (a *alerter) setKinds(k config.AlertKinds) {
	if a == nil {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	a.kinds = k
}

func (a *alerter) enabled() bool {
	if a == nil {
		return false
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.on
}

// wants reports whether one category is switched on, with the master switch
// applied first. Every emission point goes through this.
func (a *alerter) wants(pick func(config.AlertKinds) bool) bool {
	if a == nil {
		return false
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.on && pick(a.kinds)
}

// check announces whatever changed since the previous cycle.
func (a *alerter) check(m status.Model, now time.Time) {
	for _, al := range a.pending(m, now) {
		a.send(al)
	}
}

// pending is the decision half: it returns the alerts this cycle owes and
// remembers the health it was shown. Separate from delivery so the rules can be
// tested exactly, with no goroutine or desktop anywhere near them.
func (a *alerter) pending(m status.Model, now time.Time) []alert {
	if !a.enabled() {
		// Nothing is recorded while alerts are off, so switching them on later
		// starts fresh and reports a problem that is already there — rather
		// than silently comparing against history nobody was told about.
		return nil
	}
	a.mu.Lock()
	prev := a.prev
	a.mu.Unlock()

	cur := health{
		broken:     make(map[string]bool, len(m.Targets)),
		failedJobs: make(map[string]bool, len(m.Archives)),
		pending:    make(map[string]bool, len(m.PendingSources)),
	}

	// Each category is read once, so a config reload mid-cycle cannot announce
	// a fault under one setting and withhold its all-clear under another.
	wantBroken := a.wants(config.AlertKinds.BackupsStoppedOn)
	wantSnapshots := a.wants(config.AlertKinds.SnapshotFailedOn)
	wantPairs := a.wants(config.AlertKinds.PairRequestsOn)

	var out []alert
	for _, t := range m.Targets {
		if !wantBroken {
			continue
		}
		wasBroken := prev != nil && prev.broken[t.Name]
		switch {
		case brokenState(t.State) && !wasBroken:
			cur.broken[t.Name] = true
			out = append(out, targetBrokenAlert(t, now))
		case wasBroken && workingState(t.State):
			// The counterpart of a sticky "backups are not reaching X":
			// somebody who was interrupted by that should not have to go and
			// check whether it came back.
			out = append(out, alert{
				urgency: notify.Normal,
				title:   t.Name + " is backing up again",
				body:    "Backups to this destination have resumed.",
			})
		default:
			cur.broken[t.Name] = wasBroken
		}
	}

	for _, j := range m.Archives {
		if !wantSnapshots {
			continue
		}
		wasFailed := prev != nil && prev.failedJobs[j.Name]
		switch {
		case j.State == "failed" && !wasFailed:
			cur.failedJobs[j.Name] = true
			body := "The scheduled snapshot to " + j.Target + " did not complete."
			if j.Detail != "" {
				body += " " + j.Detail
			}
			out = append(out, alert{
				urgency: notify.Critical,
				title:   "Snapshot " + j.Name + " failed",
				body:    body,
			})
		case wasFailed && j.State == "ok":
			// The all-clear for the sticky alert above. "ok" and nothing else:
			// it is the one state that means a later run of this job actually
			// worked. A job that is merely due, or waiting for its password,
			// has not made a snapshot yet.
			out = append(out, alert{
				urgency: notify.Normal,
				title:   "Snapshot " + j.Name + " completed",
				body:    "A later run of this scheduled snapshot succeeded. " + j.Target + " has a fresh copy again.",
			})
		default:
			cur.failedJobs[j.Name] = wasFailed
		}
	}

	// A machine asking to pair is news, not history: on the first cycle the
	// request is simply recorded. Anything already waiting when the daemon
	// started has been waiting on the dashboard all along, and a daemon restart
	// must not replay it.
	for _, p := range m.PendingSources {
		cur.pending[p.DeviceID] = true
		if !wantPairs || prev == nil || prev.pending[p.DeviceID] {
			continue
		}
		who := p.Name
		if who == "" {
			who = "A computer on your network"
		}
		out = append(out, alert{
			urgency: notify.Normal,
			title:   who + " wants to back up to this computer",
			body:    "Approve it on the backup-maker dashboard to let it start.",
		})
	}

	a.mu.Lock()
	a.prev = &cur
	a.mu.Unlock()
	return out
}

// foreignStorage announces storage that is not the destination it should be —
// a reformatted card, or a stranger's stick at the same mount point. Nothing is
// written there, not even the status page, so this is the only way the user
// finds out.
//
// Driven from mayWrite rather than from the model, for two reasons: the model
// cannot see it (a destination whose marker has gone reads as merely
// "offline"), and mayWrite already decides when this is news — once, on the
// transition in, re-armed when the real storage comes back.
func (a *alerter) foreignStorage(name, where string) {
	if !a.wants(config.AlertKinds.UnrecognisedStorageOn) {
		return
	}
	a.send(alert{
		urgency: notify.Critical,
		title:   "Unrecognised storage where " + name + " should be",
		body: "Nothing is being written to " + where +
			", because that is not the storage backup-maker knows. If you replaced or reformatted it, set it up again.",
	})
}

// storageRecognized is the all-clear for foreignStorage, and the reason that
// alert may be sticky at all: an alert that stays on screen until it is
// dismissed, with nothing ever to withdraw it, sends the user to check by hand.
//
// Normal urgency, like every other piece of good news here. Called only on the
// transition back — mayWrite knows the moment the real storage returns — so a
// destination that was never foreign says nothing.
func (a *alerter) storageRecognized(name, where string) {
	if !a.wants(config.AlertKinds.UnrecognisedStorageOn) {
		return
	}
	a.send(alert{
		urgency: notify.Normal,
		title:   name + " is recognised again",
		body:    "The storage backup-maker knows is back at " + where + ". Backups to it have resumed.",
	})
}

// send delivers one notification on a goroutine of its own.
//
// THE STATUS LOOP MUST NOT WAIT FOR A DESKTOP. notify-send on a box with a
// session bus but no notification service sits out the D-Bus timeout before
// failing; osascript and powershell have their own ways of being slow. The
// notifier bounds itself, and this bounds the daemon: alerts are transitions,
// so a handful of goroutines a day is the whole cost.
func (a *alerter) send(al alert) {
	go func() {
		defer func() {
			// A notification must never be the thing that takes the daemon
			// down. Nothing here is expected to panic; the point is that a
			// backup process cannot afford to find out it was wrong.
			if r := recover(); r != nil {
				a.log.Debug("desktop notification panicked", "recovered", r)
			}
		}()
		sink := a.sink()
		if sink == nil {
			return
		}
		fan, ok := sink.(notify.Multi)
		if !ok {
			if err := sink.Notify(context.Background(), al.urgency, al.title, al.body); err != nil {
				a.log.Debug("could not deliver an alert", "title", al.title, "err", err)
			}
			return
		}
		results := fan.Deliver(context.Background(), al.urgency, al.title, al.body)
		for _, r := range results {
			if r.Err == nil {
				continue
			}
			// LEVEL BY METHOD, deliberately. A desktop notification failing on
			// a machine with no desktop — a Raspberry Pi target — is the normal
			// way to run this program, so it stays at debug. A WEBHOOK failing
			// never is: somebody configured an address, and if the POST is not
			// arriving then alerts are not leaving this machine at all, which
			// is precisely the silence this feature exists to break.
			if r.Method == "desktop" {
				a.log.Debug("could not show a desktop notification", "title", al.title, "err", r.Err)
				continue
			}
			a.log.Warn("an alert could not be delivered; this delivery method is not working",
				"method", r.Method, "title", al.title, "err", r.Err)
		}
		if a.delivered != nil {
			a.delivered(results)
		}
	}()
}

// brokenState reports a destination that is not backing anything up and will
// not start on its own.
//
// "offline" is deliberately absent. A laptop's card is unplugged several times
// a day and a NAS sleeps every night; interrupting the user for that would
// train them to ignore the one alert that matters. Only sustained failure —
// unreachable past stale_after_days, or full with nothing left that may be
// deleted — is worth a screen.
func brokenState(state string) bool {
	return state == "stale" || state == "full"
}

// workingState reports a destination that is actually backing up again, as
// opposed to one that merely stopped being broken by being unplugged.
func workingState(state string) bool {
	return state == "in sync" || state == "syncing" || state == "scanning"
}

func targetBrokenAlert(t status.TargetInfo, now time.Time) alert {
	if t.State == "full" {
		return alert{
			urgency: notify.Critical,
			title:   t.Name + " is full",
			body:    "There is no room left and no old backup history that may be deleted. Changes are no longer being backed up there.",
		}
	}
	body := "This destination has not been reachable for long enough that backups there are out of date."
	if !t.LastSeen.IsZero() {
		body = "Last seen " + humanAgo(now.Sub(t.LastSeen)) + ". Nothing has been backed up there since."
	}
	return alert{
		urgency: notify.Critical,
		title:   "Backups are not reaching " + t.Name,
		body:    body,
	}
}

// testDeliver sends one notification RIGHT NOW and reports whether it actually
// got out.
//
// Deliberately not the path real alerts take. Those are fire-and-forget on a
// goroutine, because nothing about a backup should wait on a desktop; a test is
// the one case where the answer IS the point, so it blocks and returns the
// delivery error instead of logging it. That is what makes the button worth
// pressing: on a headless machine, or a desktop with no notification service,
// it says so — and that is precisely the machine where silent alerting would
// otherwise be discovered on the day it was needed.
//
// It ignores whether alerts are switched on. Somebody pressing "test" is asking
// whether delivery works, and a button that quietly did nothing because of a
// setting elsewhere on the same page would teach them nothing.
// The sink is passed in rather than taken from the fan-out: the button is per
// delivery method, so "test the webhook" must exercise the webhook and not
// quietly succeed because the desktop popup worked.
func (a *alerter) testDeliver(ctx context.Context, sink notify.Notifier) error {
	if sink == nil {
		return errors.New("that delivery method is not configured")
	}
	return sink.Notify(ctx, notify.Normal,
		"backup-maker test",
		"This is what an alert from backup-maker looks like. Your backups are fine.")
}

// lanDeviceWaiting announces a device asking to watch backups from the network.
//
// Rides the pair-request category rather than inventing a new switch: to the
// person being interrupted these are the same event — something on the network
// is asking for access and is waiting on an answer — and a settings panel with
// one row per internal code path is how a preferences screen becomes unusable.
// Normal urgency: it is a request, not a fault.
func (a *alerter) lanDeviceWaiting(code, kind string) {
	if !a.wants(config.AlertKinds.PairRequestsOn) {
		return
	}
	a.send(alert{
		urgency: notify.Normal,
		title:   kind + " wants to see backup status",
		body:    "Approve code " + code + " on the backup-maker dashboard to let it watch.",
	})
}
