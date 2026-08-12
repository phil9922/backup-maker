// SPDX-License-Identifier: MIT

package daemon

import (
	"fmt"
	"sync"
	"time"

	"github.com/phil9922/backup-maker/internal/config"
	"github.com/phil9922/backup-maker/internal/notify"
	"github.com/phil9922/backup-maker/internal/status"
)

// deliveryLog remembers how each delivery method last performed.
//
// WHY THIS EXISTS AT ALL. Alerting that has quietly stopped working is the
// worst state this program can be in: everything looks fine, and the one
// mechanism meant to tell you otherwise is the thing that is broken. A webhook
// address that was right last month and is dead today produces exactly that,
// and nothing else in the system would ever mention it.
//
// In memory rather than in state.json, on purpose: this describes whether
// delivery works RIGHT NOW. Restoring "the webhook failed three days ago" after
// a restart would be reporting history as if it were a current fault.
type deliveryLog struct {
	mu   sync.Mutex
	last map[string]status.DeliveryInfo
}

func newDeliveryLog() *deliveryLog {
	return &deliveryLog{last: map[string]status.DeliveryInfo{}}
}

// record stores the outcome of one alert's delivery, per method.
func (d *deliveryLog) record(results []notify.Result) {
	if d == nil {
		return
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	for _, r := range results {
		info := status.DeliveryInfo{Method: r.Method, At: r.At, OK: r.Err == nil}
		if r.Err != nil {
			info.Error = r.Err.Error()
		}
		d.last[r.Method] = info
	}
}

// snapshot is what the dashboard renders beneath each delivery method.
func (d *deliveryLog) snapshot() []status.DeliveryInfo {
	if d == nil {
		return nil
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	out := make([]status.DeliveryInfo, 0, len(d.last))
	for _, info := range d.last {
		out = append(out, info)
	}
	// Stable order so the panel does not shuffle between polls.
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j].Method < out[j-1].Method; j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}

// recordAlert keeps one raised alert in the history and writes it out.
//
// Persisted immediately rather than batched like the byte counters, because the
// events worth reading back are exactly the ones a machine may not survive: a
// destination going stale before a crash, a snapshot failing before a reboot.
// Alerts fire on transitions, so this is a handful of writes a day rather than
// the per-file storm the tally exists to avoid.
//
// Takes d.mu because it mutates the state the rest of the daemon shares, and
// takes it AFTER delivery has finished rather than around it — a desktop that
// never answers must not be holding the lock the watchdog reads liveness from.
func (d *daemon) recordAlert(r config.AlertRecord) {
	d.mu.Lock()
	defer d.mu.Unlock()
	// Recorded onto the history AS THE FILE HAS IT, not onto ours: another
	// process may have dismissed a row since we last read it, and re-appending
	// our copy would put it back. RecordAlert already keeps the list in time
	// order, so a record arriving beside one written elsewhere lands correctly.
	if err := d.updateState(func(s *config.State) error {
		s.RecordAlert(r)
		return nil
	}); err != nil {
		d.log.Debug("could not save the alert history", "err", err)
	}
}

// dismissAlert hides one alert from the dashboard, keyed on the instant it was
// raised. Dismissed records are still published and still listed by
// `backup-maker alerts` — see AlertRecord.DismissedAt for why hiding and
// deleting are deliberately different things here.
func (d *daemon) dismissAlert(at time.Time) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	// Dismissed on the history as the FILE has it: `backup-maker alerts` may
	// have recorded one since we last read, and the whole point of dismissing is
	// that it sticks.
	return d.updateState(func(s *config.State) error {
		changed, found := s.DismissAlertsAt(at, time.Now())
		if found == 0 {
			// Saying so rather than answering ok: a dashboard whose Dismiss
			// button reports success while the row stays put is the shape of bug
			// that gets reported as "it doesn't work" with nothing in any log.
			return fmt.Errorf("no alert was raised at %s", at.Format(time.RFC3339Nano))
		}
		if changed == 0 {
			return config.ErrStateUnchanged // already dismissed, and not a failure
		}
		return nil
	})
}

// dismissAlertsBefore hides everything raised up to a moment — "Dismiss all",
// bounded by the time the button was pressed so an alert raised since then
// survives to be read.
func (d *daemon) dismissAlertsBefore(cutoff time.Time) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.updateState(func(s *config.State) error {
		if s.DismissAlertsBefore(cutoff, time.Now()) == 0 {
			return config.ErrStateUnchanged // nothing to do is not a failure
		}
		return nil
	})
}

// alertHistory is what the dashboard and `backup-maker alerts` read, newest
// first — the order somebody scanning for "what just happened" wants, and the
// reverse of how it is stored.
//
// Dismissed records are INCLUDED. The dashboard drops them, because that is
// what dismissing them meant; the CLI keeps them, because the history is the
// one place a raised-and-delivered-nowhere alert is visible at all.
func (d *daemon) alertHistory() []config.AlertRecord {
	d.mu.Lock()
	defer d.mu.Unlock()
	out := make([]config.AlertRecord, 0, len(d.state.RecentAlerts))
	for i := len(d.state.RecentAlerts) - 1; i >= 0; i-- {
		out = append(out, d.state.RecentAlerts[i])
	}
	return out
}
