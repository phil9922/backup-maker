// SPDX-License-Identifier: MIT

package daemon

import (
	"encoding/json"
	"path"
	"time"

	"github.com/phil9922/backup-maker/internal/config"
	"github.com/phil9922/backup-maker/internal/webui"
)

// manualDirName holds the manual, as ordinary web pages, inside this machine's
// own directory at a destination.
//
// WHY IT IS DROPPED HERE AT ALL: the manual is built into the binary and read in
// the dashboard, which is served by the computer being backed up — so it is
// unreachable in the one situation it was written for. Somebody holding a backup
// drive, with the computer that wrote it dead, has the backups and no
// instructions for getting them back. A copy beside the status page costs a
// couple of megabytes once per version and answers that.
//
// A DOT NAME, LIKE THE MANIFEST BESIDE IT, and for a harder reason than
// tidiness. A folder's live mirror lands at <machine>/<label> (config.DestRoot),
// so any ordinary name here is one a folder label could collide with — and a
// folder sharing its directory with these files would version every one of them
// away on its next pass, then be handed them again by the next daemon start.
// Nothing stops a label from being ".backup-maker-manual" either, but a label
// that looks like this is a deliberate act rather than an accident, and the same
// reasoning already protects the manifest.
const manualDirName = ".backup-maker-manual"

// manualStampName says which manual is in that directory. It is what makes the
// question "is the current one already there?" a single small read rather than a
// comparison of thirty files.
const manualStampName = ".backup-maker-manual.json"

// manualHref is where the manual sits relative to a machine's status page. Both
// live in the machine's directory, so the page can link to it with no idea where
// on the destination it is.
const manualHref = manualDirName + "/index.html"

// manualRecheckEvery is how often a destination is ASKED about its manual.
//
// The answer is "the current one is already here" every time but the first, and
// asking costs a round trip on the once-a-minute status cycle, so the question
// is rationed. It rations the failing case too: a destination that refuses the
// write is not handed the whole manual again a minute later.
//
// DELIBERATELY NOT statusHeartbeat, though it was at first. Tying the two
// together means the manual can only ever arrive on a cycle that was going to
// rewrite the page anyway — which hides whether the link is really part of what
// the page says, or merely turns up with the next heartbeat. They answer
// different questions and the coincidence made one of them untestable.
//
// Five minutes: one small read per destination per five minutes, against a
// status index that already lists the destination root every single cycle. It is
// also how quickly a manual somebody deleted, or a write that failed while a
// drive was busy, puts itself right.
const manualRecheckEvery = 5 * time.Minute

// manualStamp is the record left beside the pages.
//
// The fingerprint is what decides anything; the version and the time are there
// for the person who opens the file wondering how old what they are reading is.
type manualStamp struct {
	Fingerprint string    `json:"fingerprint"`
	Version     string    `json:"version"`
	Written     time.Time `json:"written"`
	Machine     string    `json:"machine"`
}

// writeManual keeps a copy of the manual in this machine's directory on one
// destination, and reports whether the current one is there now.
//
// Best-effort in exactly the way the status page is: a destination that is
// offline, full or refusing writes keeps whatever it already had, and nothing
// here may disturb backing up. The caller has already established that writing
// here at all is allowed — this must only ever be reached through mayWriteAs.
//
// The return value is used, not decorative: the status page links to the manual
// only where it actually is. A page promising a manual on a drive that has not
// got one is the small version of the lie this program exists not to tell.
func (d *daemon) writeManual(b namedBackend, machineDir string, now time.Time) bool {
	fingerprint, build := webui.DocsFingerprint, webui.BuildDocs
	if d.docsID != nil {
		fingerprint = d.docsID
	}
	if d.docsBuild != nil {
		build = d.docsBuild
	}
	id, err := fingerprint()
	if err != nil {
		// A build carrying no documentation has nothing to offer. Not a fault:
		// it is what every test binary and any trimmed build looks like.
		return false
	}
	if d.written == nil {
		d.written = map[string]lastWritten{}
	}
	was := d.written[b.name]
	// AT MOST ONE LOOK PER INTERVAL, WHATEVER THE LAST ANSWER WAS. Both answers
	// have to be rationed and for different reasons: "it is already there" is
	// the common case and would otherwise spend a round trip a minute for ever,
	// and "the write failed" would otherwise retry megabytes a minute.
	if !was.manualAt.IsZero() && now.Sub(was.manualAt) < manualRecheckEvery {
		return was.manual == id
	}
	// Recorded on every path out, so the interval above applies to failures too.
	record := func(present string) bool {
		was.manual, was.manualAt = present, now
		d.written[b.name] = was
		return present == id
	}

	dir := path.Join(machineDir, manualDirName)
	if d.stampedManual(b, dir) == id {
		return record(id)
	}

	files, err := build()
	if err != nil {
		d.log.Debug("could not build the manual to write onto a destination",
			"target", b.name, "err", err)
		return record("")
	}
	made := map[string]bool{}
	for _, f := range files {
		// The names come from this binary's own embedded tree, so nothing here
		// can be hostile today. It is checked anyway because of WHAT a bad name
		// would do: path.Join would resolve a ".." out of this directory and the
		// write would land somewhere else on the destination — another
		// computer's directory, or the destination's own index page. The same
		// guard the hand-editable recorded paths go through.
		if !config.SafeRelPath(f.Name, 1) {
			d.log.Warn("refusing to write a manual file whose name would not stay inside this machine's directory",
				"target", b.name, "file", f.Name)
			return record("")
		}
		p := path.Join(dir, f.Name)
		if parent := path.Dir(p); !made[parent] {
			if err := b.backend.MkdirAll(parent); err != nil {
				d.log.Debug("could not create the manual's directory on a destination",
					"target", b.name, "dir", parent, "err", err)
				return record("")
			}
			made[parent] = true
		}
		if err := b.backend.WriteFile(p, f.Data); err != nil {
			d.log.Debug("could not write the manual onto a destination",
				"target", b.name, "file", f.Name, "err", err)
			return record("")
		}
	}
	// THE STAMP GOES ON LAST, so a write that died halfway is repeated rather
	// than remembered as complete. Note this is the opposite of the marker an
	// export writes into a local directory, which goes down FIRST: that one is a
	// recognition gate whose job is to let a retry through, and this one is a
	// claim that all thirty files arrived.
	stamp, err := json.Marshal(manualStamp{
		Fingerprint: id,
		Version:     d.runningVersion(),
		Written:     now,
		Machine:     path.Base(machineDir),
	})
	if err != nil {
		return record("")
	}
	if err := b.backend.WriteFile(path.Join(dir, manualStampName), stamp); err != nil {
		d.log.Debug("could not stamp the manual on a destination", "target", b.name, "err", err)
		return record("")
	}
	d.log.Info("wrote the manual onto a destination so the backups can be understood without this computer",
		"target", b.name, "files", len(files), "version", d.runningVersion())
	return record(id)
}

// stampedManual is the fingerprint of the manual a destination already holds, or
// "" if there is none it can vouch for.
func (d *daemon) stampedManual(b namedBackend, dir string) string {
	raw, err := b.backend.ReadFile(path.Join(dir, manualStampName))
	if err != nil {
		return ""
	}
	var s manualStamp
	if err := json.Unmarshal(raw, &s); err != nil {
		return ""
	}
	return s.Fingerprint
}
