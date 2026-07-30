// SPDX-License-Identifier: MIT

package daemon

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"path"
	"time"

	"github.com/phil9922/backup-maker/internal/archive"
	"github.com/phil9922/backup-maker/internal/config"
	"github.com/phil9922/backup-maker/internal/localmirror"
	"github.com/phil9922/backup-maker/internal/status"
	"github.com/phil9922/backup-maker/internal/statuspage"
)

// statusWriteEvery is how often the page on each destination is RECONSIDERED.
// Frequent enough that a change of state reaches the page while it still
// matters; a tick that finds nothing changed now costs a render and no write.
const statusWriteEvery = time.Minute

// statusHeartbeat is the longest the page may go unwritten while nothing
// changes. It exists because the page's own freshness is judged by its mtime —
// both by the index beside it and by the banner the page draws in the reader's
// browser — so silence has to be bounded well inside statuspage.StaleAfter or
// skipping a write would turn a healthy machine stale by arithmetic.
//
// Four times inside the hour: three consecutive failed writes still leave the
// page trusted, and it is the fifteenfold reduction that was the point.
const statusHeartbeat = 15 * time.Minute

// lastWritten is what a destination already holds, so an unchanged page is left
// alone. Fingerprints rather than the content: this is compared once a minute
// per destination for as long as the daemon runs.
type lastWritten struct {
	page    string
	pageAt  time.Time
	index   string
	indexAt time.Time
	// manual is the fingerprint of the manual this destination is believed to
	// hold, "" for none, and manualAt is when it was last ASKED — recorded
	// whatever the answer, because both answers have to be rationed. See
	// manualRecheckEvery.
	manual   string
	manualAt time.Time
}

// due reports whether something whose content now fingerprints as fp needs
// writing: because it says something different, or because the heartbeat is up.
func due(fp, was string, at, now time.Time) bool {
	return fp != was || now.Sub(at) >= statusHeartbeat
}

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
	d.cycle(collect)
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			d.sampleSpace()
			d.cycle(collect)
		}
	}
}

// cycle collects the health model once and puts it to both uses: the page left
// on each destination, and the desktop alerts.
//
// One collection for both, so what the user is told and what the page says can
// never be a minute apart. Alerts go first, and only ever hand work to a
// goroutine, so a desktop that never answers cannot delay the page.
func (d *daemon) cycle(collect func() status.Model) {
	m := collect()
	// One clock for both, for the same reason there is one collection: the alert
	// and the page are the same report, and they should not be able to disagree
	// about when it was made.
	now := time.Now()
	d.alerts.check(m, now)
	d.writeStatusPages(m, now)
}

// writeStatusPages renders this machine's page once and writes it into this
// machine's directory on every reachable destination, then rebuilds the index
// at each destination root.
func (d *daemon) writeStatusPages(m status.Model, now time.Time) {
	built, fp := buildPage(m, now)
	machine := m.MachineName
	dir := config.MachineDir(machine)
	if d.written == nil {
		d.written = map[string]lastWritten{}
	}

	for _, b := range d.statusBackends() {
		// The page names this machine, its folders and its destinations, so it
		// goes only where the backups themselves would go — and only into the
		// directory that is actually ours.
		if !d.mayWriteAs(b.name, b.where, b.backend, b.uuid, dir) {
			continue
		}
		// Before the page, so the page can say truthfully whether the manual is
		// beside it. Nearly always a no-op: see manualRecheckEvery.
		p, pfp := built, fp
		if d.writeManual(b, dir, now) {
			p.Manual = manualHref
			// The link is part of what the page says, so it is part of what
			// decides whether the page is rewritten. A destination that has just
			// been given the manual needs a page that points at it, and on an
			// otherwise idle machine nothing else would earn that write for up
			// to a heartbeat.
			pfp = fp + "\x00manual"
		}
		was := d.written[b.name]
		// Nothing to say that this destination is not already saying. Skipped
		// before MkdirAll and the write, which are the two round trips.
		//
		// THE WRITE THIS AVOIDS IS THE COMMON CASE, not a rare one: a machine
		// whose backups are all healthy and idle produced a byte-different page
		// every minute for ever, because the page carried "4 minutes ago" text
		// that aged on its own. On a destination that is an SD card — a Pi's boot
		// card, say — that was thousands of rewrites a day of a file nobody had
		// asked a new question of.
		if !due(pfp, was.page, was.pageAt, now) {
			// Still reconsider the index: another machine's page can have gone
			// quiet, and noticing that is not conditional on ours changing.
			d.writeStatusIndex(b, now)
			continue
		}
		// Rendered per destination rather than once for all of them, because the
		// manual is per destination. The MODEL is still collected once (see
		// cycle), which is the part that must not be able to disagree with
		// itself; this is a template execution with no I/O in it.
		page, err := statuspage.Render(p)
		if err != nil {
			d.log.Warn("could not render the destination status page", "err", err)
			return
		}
		// A destination that is offline simply doesn't get an update; its page
		// keeps the last thing this machine knew, which is exactly what it is
		// for. Failures here must never disturb backing up.
		if err := b.backend.MkdirAll(dir); err != nil {
			d.log.Debug("could not create this machine's directory for the status page", "target", b.name, "err", err)
			continue
		}
		if err := b.backend.WriteFile(statuspage.PathFor(machine), page); err != nil {
			d.log.Debug("could not write the status page", "target", b.name, "err", err)
			continue
		}
		// Recorded only on success, so a destination that refused the write is
		// tried again next tick rather than being treated as up to date.
		was.page, was.pageAt = pfp, now
		d.written[b.name] = was
		d.writeStatusIndex(b, now)
	}
}

// writeStatusIndex rebuilds the destination-root page that lists every computer
// keeping backups here.
//
// Gated on mayWrite — the marker alone, not the claim. The index republishes
// only what the destination already holds, so a machine refused a name it does
// not own may still keep the index honest; and if it could not, a destination
// shared by two computers would show only whichever of them happened to own the
// name. Unrecognised storage still gets nothing, which is the gate that matters.
func (d *daemon) writeStatusIndex(b namedBackend, now time.Time) {
	if !d.mayWrite(b.name, b.where, b.backend, b.uuid) {
		return
	}
	dirs, err := localmirror.TopLevelDirs(b.backend)
	if err != nil {
		d.log.Debug("could not list the destination to build its status index", "target", b.name, "err", err)
		return
	}
	var entries []statuspage.IndexEntry
	var nested []string
	for _, dir := range dirs {
		if dir == config.VersionsDirName || dir == archive.DirName {
			continue
		}
		// A destination can be a FOLDER on a drive, and that folder keeps an
		// index page of its own. It is named further down the page rather than
		// listed among the machines: it is a separate destination, and its
		// health is its own page's to report. The marker is what tells the two
		// apart — a machine directory never has one.
		if _, err := b.backend.Stat(path.Join(dir, localmirror.MarkerName)); err == nil {
			if _, err := b.backend.Stat(path.Join(dir, statuspage.FileName)); err == nil {
				nested = append(nested, dir)
			}
			continue
		}
		fi, err := b.backend.Stat(path.Join(dir, statuspage.FileName))
		if err != nil {
			continue // not a machine directory, or one that has never reported
		}
		entries = append(entries, statuspage.IndexEntry{
			Machine: dir,
			Written: fi.ModTime(),
			Stale:   now.Sub(fi.ModTime()) > statuspage.StaleAfter,
		})
	}
	// STALE IS IN THE FINGERPRINT, and it is the reason this cannot simply skip
	// whenever no page changed. A machine that goes quiet crosses into stale by
	// the passage of time alone: nothing about it is written, no mtime moves, and
	// the only thing that changes is the answer to "has it been an hour". Leave
	// that out and the index would go on saying a machine that stopped reporting
	// two days ago is fine, which is the one lie this program must never tell.
	fp := indexFingerprint(entries, nested)
	was := d.written[b.name]
	if !due(fp, was.index, was.indexAt, now) {
		return
	}
	index, err := statuspage.RenderIndex(entries, nested, now)
	if err != nil {
		d.log.Warn("could not render the destination status index", "err", err)
		return
	}
	if err := b.backend.WriteFile(statuspage.FileName, index); err != nil {
		d.log.Debug("could not write the status index", "target", b.name, "err", err)
		return
	}
	was.index, was.indexAt = fp, now
	if d.written == nil {
		d.written = map[string]lastWritten{}
	}
	d.written[b.name] = was
}

// indexFingerprint is what the index says: which machines report here, when each
// last did, whether that is too long ago, and which nested destinations are
// named. Every one of those is a fact about the destination rather than about
// the clock, so an index that fingerprints the same is one nobody would read
// differently.
func indexFingerprint(entries []statuspage.IndexEntry, nested []string) string {
	h := sha256.New()
	for _, e := range entries {
		fmt.Fprintf(h, "machine\x00%s\x00%d\x00%t\x00", e.Machine, e.Written.UnixNano(), e.Stale)
	}
	for _, n := range nested {
		fmt.Fprintf(h, "nested\x00%s\x00", n)
	}
	return hex.EncodeToString(h.Sum(nil))
}

// buildPage converts the status model into the redacted shape the page shows.
//
// Folder LABELS and destination NAMES only: the file lives on shared storage,
// so it carries health rather than a description of this machine's filesystem.
// It returns the page and a fingerprint of everything on it EXCEPT the values
// that age by themselves — the "4 minutes ago" text, and the written-at stamp.
// Two pages with the same fingerprint tell a reader the same thing, so there is
// no reason to spend a write replacing one with the other.
//
// The fingerprint is computed here, beside the fields going onto the page,
// rather than in a function of its own that reads the finished struct. A field
// added to the page and forgotten by the fingerprint would be a field that never
// reaches the destination once it settles — a silent staleness, and the worst
// kind. Next to each other, the omission is visible while it is being made.
//
// What ticking values cost by being left out: a page can be up to
// statusHeartbeat behind on how-long-ago text. It cannot be behind on a STATE —
// any state, storage figure or snapshot result that differs lands within
// statusWriteEvery, as before.
func buildPage(m status.Model, now time.Time) (statuspage.Page, string) {
	p := statuspage.Page{Machine: m.MachineName, Written: now}
	h := sha256.New()
	add := func(parts ...any) {
		for _, v := range parts {
			fmt.Fprintf(h, "%v\x00", v)
		}
	}
	add("machine", m.MachineName)
	for _, r := range m.Rows {
		label := r.FolderLabel
		if label == "" {
			label = r.FolderID
		}
		detail, ticking := rowDetail(r, now)
		// The words shown, not the engine's internal name: this page is read by
		// somebody standing at a destination with the computer switched off, and
		// it has to use the same vocabulary as everything else.
		p.Rows = append(p.Rows, statuspage.Row{
			Folder:      label,
			Destination: r.TargetName,
			State:       status.RowLabel(r),
			Health:      status.RowHealth(r),
			Detail:      detail,
		})
		// FINGERPRINTED ON WHAT IT SAYS, not on the state behind it. Several
		// engine states share one word — "in sync" and a "scanning" pass over a
		// folder already backed up both read "backed up" — and a page whose words
		// have not changed is a page nobody would read differently, so it is not
		// worth a write to a destination.
		add("row", label, r.TargetName, status.RowLabel(r), status.RowHealth(r))
		if !ticking {
			add(detail)
		}
	}
	for _, t := range m.Targets {
		// A destination that cannot be measured says so. Omitting the row would
		// leave it reading as a destination with nothing to report, which is
		// exactly what a healthy paired machine looks like.
		if t.SpaceUnknown {
			p.Storage = append(p.Storage, statuspage.StorageLine{
				Destination: t.Name, Unavailable: true,
			})
			add("storage", t.Name, "unavailable")
			continue
		}
		if t.TotalBytes == 0 {
			continue
		}
		used := t.TotalBytes - t.FreeBytes
		line := statuspage.StorageLine{
			Destination: t.Name,
			Free:        humanBytes(int64(t.FreeBytes)),
			Total:       humanBytes(int64(t.TotalBytes)),
			UsedPct:     int(used * 100 / t.TotalBytes),
		}
		p.Storage = append(p.Storage, line)
		// The DISPLAYED figures, not the raw byte counts. Free space moves
		// constantly on a destination being written to, and hashing the exact
		// number would put the page back to a write a minute while saying "95GB
		// free" every time. Rounded to what the reader sees, it changes when the
		// answer changes.
		add("storage", line.Destination, line.Free, line.Total, line.UsedPct)
	}
	for _, a := range m.Archives {
		last, ticking := "never", false
		if !a.LastRun.IsZero() {
			last, ticking = humanAgo(now.Sub(a.LastRun)), true
		}
		p.Snapshots = append(p.Snapshots, statuspage.Row{
			Folder: a.Name, Destination: a.Target, Detail: last,
			State:  status.ArchiveLabel(a),
			Health: status.ArchiveHealth(a),
		})
		add("snapshot", a.Name, a.Target, status.ArchiveLabel(a), status.ArchiveHealth(a))
		if !ticking {
			add(last) // "never" is a fact about the schedule, not about the clock
		}
	}
	return p, hex.EncodeToString(h.Sum(nil))
}

// rowDetail is what a row says beyond its state, and whether that text is a
// clock that will read differently in a minute's time through nothing having
// happened. The caller needs the two apart to know whether a rewrite would tell
// anybody anything.
func rowDetail(r status.Row, now time.Time) (text string, ticking bool) {
	if r.State == "syncing" && r.TotalBytes > 0 {
		// Mid-transfer progress. Not ticking: it moves because bytes are moving,
		// and a page being read during a transfer should follow it.
		return humanBytes(r.TransferredBytes) + " of " + humanBytes(r.TotalBytes), false
	}
	if r.LastSeen.IsZero() {
		return "", false
	}
	return humanAgo(now.Sub(r.LastSeen)), true
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
// failure can say which destination it was. It carries the UUID recorded for
// that target too: the connection alone cannot say whether the storage on the
// other end of it is still ours.
type namedBackend struct {
	name    string
	where   string // mount path or share URL, for messages
	uuid    string // marker UUID recorded for this target
	backend localmirror.Backend
}

// mayWrite reports whether ANY file of ours may be put at this destination —
// the adoption manifest and the status page included, not just mirrored data.
//
// It asks localmirror.Recognize, the same question the mirror engine asks before
// it copies anything, so the two cannot drift into disagreeing. They must not:
// the manifest names this machine, its source folder paths (which carry the
// user's account name) and every destination it backs up to, and the status page
// names its folders. Storage we refuse to back up to must not be told any of
// that, and a user told a target is offline must not find files on it.
//
// Refusal is reported once per destination, on the transition into it, the way
// recordSample reports an unmeasurable one: this runs once a minute for ever. A
// destination that comes good clears the flag, so a foreign drive appearing
// again is reported again; a foreign drive merely unplugged does not re-arm it.
// mayWriteAs is mayWrite plus the second question a shared destination forces:
// not only "is this our storage" but "is this directory on it ours".
//
// Both are needed and neither implies the other. The marker says the drive is
// the one this target was set up against — which it is, for every computer that
// shares it. The claim says the <machine> directory belongs to this
// installation, which is what stops two computers that happen to have the same
// machine name from writing into one directory and versioning each other's
// files away.
func (d *daemon) mayWriteAs(name, where string, b localmirror.Backend, uuid, machineDir string) bool {
	if !d.mayWrite(name, where, b, uuid) {
		return false
	}
	return d.mayWriteIntoMachineDir(name, where, b, machineDir)
}

// mayWriteIntoMachineDir is the claim half of mayWriteAs.
//
// AN UNCLAIMED DIRECTORY IS TAKEN, NOT REFUSED, and the asymmetry with setup is
// deliberate. Every destination in service before this existed has no claim
// file on it. If the daemon refused those, replacing the binary would stop
// every working backup on the machine — a silent, total failure caused by an
// upgrade. Setup asks the user about the same situation because a human is
// there to answer; the daemon has nobody to ask and the safe default is the
// opposite one.
func (d *daemon) mayWriteIntoMachineDir(name, where string, b localmirror.Backend, machineDir string) bool {
	d.mu.Lock()
	state := d.state
	d.mu.Unlock()

	if state.InstallID == "" {
		// No identity to judge with. A running daemon always has one (it is
		// minted at startup beside the IPC token), so this is only reachable by
		// a caller that built a daemon by hand. Behaving as we did before claims
		// existed is the right answer: being unable to identify ourselves is not
		// a reason to stop backing up.
		return true
	}

	st, holder := localmirror.CheckClaim(b, machineDir, state.Owns)
	if st == localmirror.ClaimUnclaimed {
		var err error
		st, err = localmirror.ClaimIfUnclaimed(b, machineDir, state.InstallID, machineDir, state.Owns)
		if err != nil {
			d.log.Debug("could not claim this machine's directory", "target", name, "err", err)
			return false
		}
		if st != localmirror.ClaimOurs {
			_, holder = localmirror.CheckClaim(b, machineDir, state.Owns)
		}
	}

	d.foreignMu.Lock()
	defer d.foreignMu.Unlock()
	if st == localmirror.ClaimOurs {
		if d.clashing[name] {
			delete(d.clashing, name)
			d.log.Info("this destination's folder for this computer is ours again; writing has resumed",
				"target", name, "location", where)
			d.alerts.nameClashResolved(name, where)
		}
		return true
	}
	if d.clashing == nil {
		d.clashing = map[string]bool{}
	}
	if !d.clashing[name] {
		d.clashing[name] = true
		other := "another computer"
		if holder != nil && holder.MachineName != "" {
			other = holder.MachineName
		}
		d.log.Error("another computer is already backing up to this destination under the same name; nothing is being written there, because both computers would delete each other's files",
			"target", name, "location", where, "folder", machineDir, "claimed_by", other)
		// The health model shows this as a per-folder state, but a destination
		// with no folders assigned would show nothing at all — and this is data
		// loss averted, not a detail.
		d.alerts.nameClash(name, where, machineDir)
	}
	return false
}

func (d *daemon) mayWrite(name, where string, b localmirror.Backend, uuid string) bool {
	r := localmirror.Recognize(b, uuid)
	d.foreignMu.Lock()
	defer d.foreignMu.Unlock()
	if r == localmirror.Ours {
		if d.foreign[name] {
			delete(d.foreign, name)
			// Said on the way out as well as on the way in. The refusal is
			// sticky on screen, and an alert nothing ever withdraws leaves the
			// user checking by hand — which is what being told is meant to
			// spare them.
			d.log.Info("the storage this target expects is present again; writing has resumed",
				"target", name, "location", where)
			d.alerts.storageRecognized(name, where)
		}
		return true
	}
	if r == localmirror.Foreign {
		if d.foreign == nil {
			d.foreign = map[string]bool{}
		}
		if !d.foreign[name] {
			d.foreign[name] = true
			d.log.Warn("storage that is not this target is present at that location (its identity marker is missing or belongs to different storage); nothing is being written there, not even status information",
				"target", name, "location", where)
			// A log line on a machine nobody is watching is not being told.
			// This is the one failure the health model cannot show — the
			// destination reads as merely offline — so it is said out loud.
			d.alerts.foreignStorage(name, where)
		}
	}
	return false
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

// refusedTargets names the destinations this daemon is currently declining to
// write to, and why, in the vocabulary the rest of the program already uses.
//
// THIS IS THE BRIDGE THE HEALTH MODEL WAS MISSING. mayWrite discovers refusal
// once a minute while refreshing status pages; the mirror engine discovers the
// same thing only when it next tries to write, which is the next save or the
// hourly pass. Until this existed the knowledge went nowhere but an alert, so
// the dashboard could say "Everything is backed up" for the best part of an hour
// after the user had been told nothing was being written to a destination —
// which is the one contradiction a page whose whole job is answering "are my
// files safe" cannot afford.
//
// Its own lock, not d.mu: the status writer holds foreignMu while asking the
// same question, and this is read from the status collector.
func (d *daemon) refusedTargets() map[string]string {
	d.foreignMu.Lock()
	defer d.foreignMu.Unlock()
	if len(d.foreign) == 0 && len(d.clashing) == 0 {
		return nil
	}
	out := make(map[string]string, len(d.foreign)+len(d.clashing))
	// Foreign storage first, then the claim: a destination can only be in one
	// of the two states, but if both flags were somehow set the storage being
	// wrong is the more fundamental fact, so it is the one that must not be
	// overwritten.
	for name := range d.clashing {
		out[name] = "name-clash"
	}
	for name := range d.foreign {
		out[name] = "wrong-drive"
	}
	return out
}
