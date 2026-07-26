// SPDX-License-Identifier: MIT

package daemon

import (
	"context"
	"time"

	"github.com/phil9922/backup-maker/internal/localmirror"
	"github.com/phil9922/backup-maker/internal/status"
	"github.com/phil9922/backup-maker/internal/statuspage"
)

// statusWriteEvery is how often the page on each destination is refreshed.
// Frequent enough that "last reported" reads as live while this machine is up;
// infrequent enough to be nothing on an SSD and gentle over SMB.
const statusWriteEvery = time.Minute

// statusPageLoop keeps a readable status page on every drive/share destination.
//
// The dashboard is served by this computer, so it disappears exactly when it is
// most wanted — when this machine is off, broken or stolen. A destination that
// stays powered can hold a page that still answers, because it is only a file
// sitting beside the backups. Any device that can browse the share opens it;
// with a web server on that box, any device on the network can.
func (d *daemon) statusPageLoop(ctx context.Context, collect func() status.Model) {
	tick := time.NewTicker(statusWriteEvery)
	defer tick.Stop()

	// Sample free space before collecting, so both the status page and the
	// dashboard's cache see a fresh reading rather than last minute's.
	d.sampleSpace()
	d.writeStatusPages(collect())
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			d.sampleSpace()
			d.writeStatusPages(collect())
		}
	}
}

// writeStatusPages renders once and writes the same page to every reachable
// destination.
func (d *daemon) writeStatusPages(m status.Model) {
	page, err := statuspage.Render(buildPage(m, time.Now()))
	if err != nil {
		d.log.Warn("could not render the destination status page", "err", err)
		return
	}

	for _, b := range d.statusBackends() {
		// A destination that is offline simply doesn't get an update; its page
		// keeps the last thing this machine knew, which is exactly what it is
		// for. Failures here must never disturb backing up.
		if err := b.backend.WriteFile(statuspage.FileName, page); err != nil {
			d.log.Debug("could not write the status page", "target", b.name, "err", err)
		}
	}
}

// buildPage converts the status model into the redacted shape the page shows.
//
// Folder LABELS and destination NAMES only: the file lives on shared storage,
// so it carries health rather than a description of this machine's filesystem.
func buildPage(m status.Model, now time.Time) statuspage.Page {
	p := statuspage.Page{Machine: m.MachineName, Written: now}
	for _, r := range m.Rows {
		label := r.FolderLabel
		if label == "" {
			label = r.FolderID
		}
		p.Rows = append(p.Rows, statuspage.Row{
			Folder:      label,
			Destination: r.TargetName,
			State:       r.State,
			Detail:      rowDetail(r, now),
		})
	}
	for _, t := range m.Targets {
		// A destination that cannot be measured says so. Omitting the row would
		// leave it reading as a destination with nothing to report, which is
		// exactly what a healthy paired machine looks like.
		if t.SpaceUnknown {
			p.Storage = append(p.Storage, statuspage.StorageLine{
				Destination: t.Name, Unavailable: true,
			})
			continue
		}
		if t.TotalBytes == 0 {
			continue
		}
		used := t.TotalBytes - t.FreeBytes
		p.Storage = append(p.Storage, statuspage.StorageLine{
			Destination: t.Name,
			Free:        humanBytes(int64(t.FreeBytes)),
			Total:       humanBytes(int64(t.TotalBytes)),
			UsedPct:     int(used * 100 / t.TotalBytes),
		})
	}
	for _, a := range m.Archives {
		last := "never"
		if !a.LastRun.IsZero() {
			last = humanAgo(now.Sub(a.LastRun))
		}
		p.Snapshots = append(p.Snapshots, statuspage.Row{
			Folder: a.Name, Destination: a.Target, State: a.State, Detail: last,
		})
	}
	return p
}

func rowDetail(r status.Row, now time.Time) string {
	if r.State == "syncing" && r.TotalBytes > 0 {
		return humanBytes(r.TransferredBytes) + " of " + humanBytes(r.TotalBytes)
	}
	if r.LastSeen.IsZero() {
		return ""
	}
	return humanAgo(now.Sub(r.LastSeen))
}

func humanAgo(d time.Duration) string {
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return itoa(int64(d/time.Minute)) + " minutes ago"
	case d < 48*time.Hour:
		return itoa(int64(d/time.Hour)) + " hours ago"
	default:
		return itoa(int64(d/(24*time.Hour))) + " days ago"
	}
}

func humanBytes(n int64) string {
	switch {
	case n >= 1<<30:
		return itoa(n/(1<<30)) + "GB"
	case n >= 1<<20:
		return itoa(n/(1<<20)) + "MB"
	case n >= 1<<10:
		return itoa(n/(1<<10)) + "KB"
	default:
		return itoa(n) + "B"
	}
}

func itoa(v int64) string {
	if v == 0 {
		return "0"
	}
	neg := v < 0
	if neg {
		v = -v
	}
	var b [24]byte
	i := len(b)
	for v > 0 {
		i--
		b[i] = byte('0' + v%10)
		v /= 10
	}
	if neg {
		i--
		b[i] = '-'
	}
	return string(b[i:])
}

// namedBackend pairs a destination's name with its connection, so a write
// failure can say which destination it was.
type namedBackend struct {
	name    string
	backend localmirror.Backend
}

// statusBackends snapshots the current destinations under the lock, so a
// config reload swapping them out mid-write can't race.
func (d *daemon) statusBackends() []namedBackend {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]namedBackend(nil), d.statusPageBackends...)
}

// sampleSpace reads free/total off each destination's already-open connection
// and caches it. Runs on the statusPageLoop goroutine — the same one that
// writes the page — so it reuses the single connection and never issues a
// second SMB operation on the same session concurrently.
//
// A destination that can't be measured right now (offline, or a backend that
// doesn't implement SpaceReporter) keeps whatever was last cached: the reading
// is marked stale by its timestamp elsewhere, which is more useful than the bar
// vanishing whenever a NAS naps.
func (d *daemon) sampleSpace() {
	now := time.Now()
	for _, b := range d.statusBackends() {
		reporter, ok := b.backend.(localmirror.SpaceReporter)
		if !ok {
			continue
		}
		d.recordSample(b.name, reporter, now)
	}
}

// recordSample updates one destination's cached usage. A read that fails (or
// reports a zero total) leaves the previous entry untouched, so the last-known-
// good value and its timestamp survive an offline destination.
//
// A destination that has never answered is a different matter, and is recorded
// as failed rather than dropped: the reserve set by min_free_gb cannot be
// enforced where free space can't be read, and staying silent about that made an
// unprotected destination look identical to a healthy one.
func (d *daemon) recordSample(name string, r localmirror.SpaceReporter, now time.Time) {
	free, total, err := r.Usage()
	d.spaceMu.Lock()
	defer d.spaceMu.Unlock()
	if d.space == nil {
		d.space = map[string]spaceSample{}
	}
	if err != nil || total == 0 {
		prev, seen := d.space[name]
		if seen && !prev.failed {
			// Answered before, quiet now: the previous figures stand, marked
			// stale by their timestamp. Debug, because a napping NAS is the
			// normal case and this line lands once a minute.
			d.log.Debug("destination did not report free space; keeping the last reading",
				"target", name, "err", err)
			return
		}
		if seen {
			return // already marked; the warning below was logged on the way in
		}
		// Logged once, on entering the failed state rather than every minute:
		// a destination that never answers would otherwise repeat this forever.
		d.log.Warn("destination cannot report how much free space it has; the reserve set by min_free_gb is not being enforced there",
			"target", name, "err", err)
		d.space[name] = spaceSample{failed: true}
		return
	}
	d.space[name] = spaceSample{free: free, total: total, at: now}
}

// spaceSamples returns a copy of the cached usage, keyed by destination name,
// for the status collector.
func (d *daemon) spaceSamples() map[string]status.SpaceSample {
	d.spaceMu.Lock()
	defer d.spaceMu.Unlock()
	out := make(map[string]status.SpaceSample, len(d.space))
	for name, s := range d.space {
		out[name] = status.SpaceSample{Free: s.free, Total: s.total, At: s.at, Unknown: s.failed}
	}
	return out
}
