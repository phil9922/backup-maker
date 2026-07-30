// SPDX-License-Identifier: MIT

package daemon

import (
	"bytes"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/phil9922/backup-maker/internal/config"
	"github.com/phil9922/backup-maker/internal/localmirror"
	"github.com/phil9922/backup-maker/internal/status"
	"github.com/phil9922/backup-maker/internal/statuspage"
)

// countingFS counts writes per path, so a test can assert what a cycle did to
// the destination rather than inspecting mtimes at filesystem granularity.
type countingFS struct {
	localmirror.Backend
	writes map[string]int
	failOn string // a path whose writes are refused
}

func (c *countingFS) WriteFile(p string, data []byte) error {
	c.writes[p]++
	if c.failOn != "" && p == c.failOn {
		return errors.New("destination refused the write")
	}
	return c.Backend.WriteFile(p, data)
}

// pageWriter is one machine writing status pages to a destination it owns, with
// its writes counted and its clock supplied by the test.
type pageWriter struct {
	d     *daemon
	root  string
	fs    *countingFS
	model status.Model
}

func newPageWriter(t *testing.T) *pageWriter {
	t.Helper()
	root := t.TempDir()
	if err := localmirror.WriteMarkerAt(root, "card-uuid", "laptop"); err != nil {
		t.Fatal(err)
	}
	return newPageWriterAt(t, root)
}

// newPageWriterAt is the same machine writing to a destination that already
// exists — a second daemon over storage a previous one has been using, which is
// what an upgrade or a restart looks like from the destination's point of view.
func newPageWriterAt(t *testing.T, root string) *pageWriter {
	t.Helper()
	cfs := &countingFS{Backend: localmirror.NewLocalFS(root), writes: map[string]int{}}
	cfg := &config.Config{
		General: config.General{MachineName: "laptop"},
		Targets: []config.Target{{Type: "drive", Name: "card", Path: root, Folders: []string{}}},
	}
	d := &daemon{
		log: slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)),
		state: &config.State{
			InstallID:        "install-laptop",
			DriveTargetUUIDs: map[string]string{"card": "card-uuid"},
		},
		cfg: cfg,
	}
	d.statusPageBackends = []namedBackend{{
		name: "card", where: root, uuid: "card-uuid", backend: cfs,
	}}
	return &pageWriter{d: d, root: root, fs: cfs, model: status.Model{
		MachineName: "laptop",
		Rows: []status.Row{{
			FolderID: "dev", FolderLabel: "Development", TargetName: "card",
			State: "in sync", LastSeen: time.Date(2026, 7, 24, 9, 0, 0, 0, time.UTC),
		}},
	}}
}

func (w *pageWriter) cycleAt(at time.Time) { w.d.writeStatusPages(w.model, at) }

func (w *pageWriter) pageWrites() int  { return w.fs.writes[statuspage.PathFor("laptop")] }
func (w *pageWriter) indexWrites() int { return w.fs.writes[statuspage.FileName] }

var noon = time.Date(2026, 7, 24, 12, 0, 0, 0, time.UTC)

// THE GUARANTEE: a page saying what it already says is not written again.
//
// This is the whole point of the change. The page carried "4 minutes ago" text
// that aged on its own, so it was byte-different every minute and every minute
// it was rewritten — on an idle machine with healthy backups, for ever. Measured
// on a 128GB card that was essentially all the write traffic the card saw while
// nothing was happening, and on a Pi's boot SD card it is the same file rewritten
// a couple of thousand times a day for no reader's benefit.
func TestAnUnchangedPageIsNotRewritten(t *testing.T) {
	w := newPageWriter(t)
	w.cycleAt(noon)
	if got := w.pageWrites(); got != 1 {
		t.Fatalf("first cycle wrote the page %d times, want 1", got)
	}
	w.cycleAt(noon.Add(time.Minute))
	if got := w.pageWrites(); got != 1 {
		t.Errorf("an unchanged page was written %d times, want 1", got)
	}
}

// THE GUARANTEE, and the one that must not be traded for the above: how long ago
// is not a reason to rewrite, but a CHANGE OF STATE always is, at the next tick.
//
// A destination going offline is the moment the page exists for. If skipping
// unchanged writes could delay that, the whole change would be a loss.
func TestAChangedStateReachesThePageAtOnce(t *testing.T) {
	w := newPageWriter(t)
	w.cycleAt(noon)
	w.model.Rows[0].State = "offline"
	w.cycleAt(noon.Add(time.Minute))
	if got := w.pageWrites(); got != 2 {
		t.Errorf("a destination went offline and the page was written %d times, want 2", got)
	}
}

// THE GUARANTEE: only the clock moved, so nothing is written.
//
// The crux. Between these two cycles the page's "last seen" text really does
// change — 3 hours ago becomes 4 hours ago — and that is exactly the churn being
// eliminated. A reader anchors those phrases to the page's own written-at stamp,
// so a page that is behind on them under-claims freshness; it can never
// over-claim it, which is the direction that would matter.
func TestTextThatOnlyAgesDoesNotEarnAWrite(t *testing.T) {
	w := newPageWriter(t)
	// Last seen two minutes ago, so the minutes bucket ticks across the gap
	// below. The gap has to stay inside statusHeartbeat or the heartbeat is what
	// would be under test instead.
	w.model.Rows[0].LastSeen = noon.Add(-2 * time.Minute)
	const gap = 5 * time.Minute
	if gap >= statusHeartbeat {
		t.Fatalf("gap %s is not inside statusHeartbeat %s", gap, statusHeartbeat)
	}
	w.cycleAt(noon)

	before, _ := buildPage(w.model, noon)
	after, _ := buildPage(w.model, noon.Add(gap))
	if before.Rows[0].Detail == after.Rows[0].Detail {
		t.Fatalf("this test proves nothing: the row detail did not age (%q both times)",
			before.Rows[0].Detail)
	}

	w.cycleAt(noon.Add(gap))
	if got := w.pageWrites(); got != 1 {
		t.Errorf("the page was written %d times when only its how-long-ago text "+
			"changed (%q became %q), want 1",
			got, before.Rows[0].Detail, after.Rows[0].Detail)
	}
}

// THE GUARANTEE: silence is bounded, so a live machine never reads as stale.
//
// The page's freshness is judged by its mtime — by the index beside it and by
// the banner the page draws in the reader's browser. Skipping writes therefore
// cannot be unconditional: left alone long enough, a perfectly healthy machine
// would be declared stale by arithmetic. The heartbeat is what stops that, and
// it has to stay well inside the hour.
func TestThePageIsRewrittenLongBeforeItCouldReadAsStale(t *testing.T) {
	if statusHeartbeat >= statuspage.StaleAfter {
		t.Fatalf("statusHeartbeat (%s) is not inside statuspage.StaleAfter (%s): a "+
			"healthy machine would be reported stale", statusHeartbeat, statuspage.StaleAfter)
	}
	if statusHeartbeat > statuspage.StaleAfter/3 {
		t.Errorf("statusHeartbeat (%s) leaves less than three attempts before "+
			"statuspage.StaleAfter (%s): one failed write should not be able to "+
			"make a working machine look gone", statusHeartbeat, statuspage.StaleAfter)
	}

	w := newPageWriter(t)
	w.cycleAt(noon)
	w.cycleAt(noon.Add(statusHeartbeat - time.Second))
	if got := w.pageWrites(); got != 1 {
		t.Errorf("wrote %d times before the heartbeat was due, want 1", got)
	}
	w.cycleAt(noon.Add(statusHeartbeat))
	if got := w.pageWrites(); got != 2 {
		t.Errorf("the heartbeat did not rewrite the page: %d writes, want 2", got)
	}
}

// THE GUARANTEE: a machine that goes quiet is reported stale even though nothing
// was written to make it so.
//
// The subtle one, and the one that would be an outright lie. A machine crossing
// into staleness does so by the passage of time: it writes nothing, no mtime
// moves, and the only thing that changes is the answer to "has it been an hour".
// An index skipped because "nothing changed" would go on saying a machine that
// stopped reporting days ago is fine — while this machine's own page is being
// correctly skipped for being unchanged, so the index gets no write on its coat
// tails either.
func TestAnotherMachineGoingQuietRewritesTheIndex(t *testing.T) {
	w := newPageWriter(t)
	other := filepath.Join(w.root, "attic-pi")
	if err := os.MkdirAll(other, 0o755); err != nil {
		t.Fatal(err)
	}
	page := filepath.Join(other, statuspage.FileName)
	if err := os.WriteFile(page, []byte("<html>attic-pi</html>"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Reported 59 minutes before noon: fresh at the first cycle, stale at the
	// second, with nothing whatsoever having been written in between.
	reported := noon.Add(-59 * time.Minute)
	if err := os.Chtimes(page, reported, reported); err != nil {
		t.Fatal(err)
	}

	w.cycleAt(noon)
	first := w.indexWrites()
	if first == 0 {
		t.Fatal("the index was never written")
	}

	w.cycleAt(noon.Add(2 * time.Minute)) // now 61 minutes since attic-pi reported
	if got := w.indexWrites(); got != first+1 {
		t.Errorf("attic-pi crossed into stale and the index was written %d times, "+
			"want %d: the index would still be calling it healthy", got, first+1)
	}
	if got := w.pageWrites(); got != 1 {
		t.Errorf("this machine's own page was written %d times, want 1: the index "+
			"must be reconsidered without the page being rewritten too", got)
	}
}

// THE GUARANTEE: a refused write is attempted again, not remembered as done.
//
// Recording the fingerprint before knowing the write landed would leave a
// destination that was briefly unwritable holding a page from before whatever
// went wrong, with this machine believing it was up to date — for up to the
// heartbeat, and indefinitely if the state then settled.
func TestARefusedWriteIsTriedAgainRatherThanAssumedDone(t *testing.T) {
	w := newPageWriter(t)
	w.fs.failOn = statuspage.PathFor("laptop")

	w.cycleAt(noon)
	w.cycleAt(noon.Add(time.Minute))
	if got := w.pageWrites(); got != 2 {
		t.Errorf("a refused write was attempted %d times over two cycles, want 2", got)
	}

	w.fs.failOn = ""
	w.cycleAt(noon.Add(2 * time.Minute))
	if got := w.pageWrites(); got != 3 {
		t.Errorf("the page was not written once the destination accepted it again: "+
			"%d attempts, want 3", got)
	}
}

// A page gains fields over time, and each one has to be in the fingerprint or it
// will stop reaching settled destinations. This walks the facts the page shows
// and asserts each one, changed on its own, is noticed.
func TestEveryFactOnThePageIsNoticedWhenItChanges(t *testing.T) {
	base := status.Model{
		MachineName: "laptop",
		Rows: []status.Row{{
			FolderID: "dev", FolderLabel: "Development", TargetName: "card",
			State: "in sync", LastSeen: noon.Add(-time.Minute),
		}},
		Targets:  []status.TargetInfo{{Name: "card", Type: "drive", FreeBytes: 8 << 30, TotalBytes: 64 << 30}},
		Archives: []status.ArchiveRow{{Name: "nightly", Target: "card", State: "ok", LastRun: noon.Add(-time.Hour)}},
	}
	_, was := buildPage(base, noon)

	for _, c := range []struct {
		what   string
		change func(m *status.Model)
	}{
		{"the machine name", func(m *status.Model) { m.MachineName = "desktop" }},
		{"a folder label", func(m *status.Model) { m.Rows[0].FolderLabel = "Photos" }},
		{"a row's destination", func(m *status.Model) { m.Rows[0].TargetName = "nas" }},
		{"a row's state", func(m *status.Model) { m.Rows[0].State = "offline" }},
		{"free space", func(m *status.Model) { m.Targets[0].FreeBytes = 2 << 30 }},
		{"total space", func(m *status.Model) { m.Targets[0].TotalBytes = 128 << 30 }},
		{"a destination becoming unmeasurable", func(m *status.Model) { m.Targets[0].SpaceUnknown = true }},
		{"a snapshot's name", func(m *status.Model) { m.Archives[0].Name = "weekly" }},
		{"a snapshot's destination", func(m *status.Model) { m.Archives[0].Target = "nas" }},
		{"a snapshot's state", func(m *status.Model) { m.Archives[0].State = "failed" }},
		{"a snapshot having never run", func(m *status.Model) { m.Archives[0].LastRun = time.Time{} }},
	} {
		m := base
		m.Rows = append([]status.Row(nil), base.Rows...)
		m.Targets = append([]status.TargetInfo(nil), base.Targets...)
		m.Archives = append([]status.ArchiveRow(nil), base.Archives...)
		c.change(&m)
		if _, fp := buildPage(m, noon); fp == was {
			t.Errorf("%s changed and the page fingerprint did not: that change would "+
				"never reach a destination once the page settled", c.what)
		}
	}
}
