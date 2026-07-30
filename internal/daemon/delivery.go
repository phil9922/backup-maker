// SPDX-License-Identifier: MIT

package daemon

import (
	"sync"

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
	d.state.RecordAlert(r)
	if err := d.state.Save(); err != nil {
		d.log.Debug("could not save the alert history", "err", err)
	}
}

// alertHistory is what the dashboard and `backup-maker alerts` read, newest
// first — the order somebody scanning for "what just happened" wants, and the
// reverse of how it is stored.
func (d *daemon) alertHistory() []config.AlertRecord {
	d.mu.Lock()
	defer d.mu.Unlock()
	out := make([]config.AlertRecord, 0, len(d.state.RecentAlerts))
	for i := len(d.state.RecentAlerts) - 1; i >= 0; i-- {
		out = append(out, d.state.RecentAlerts[i])
	}
	return out
}
