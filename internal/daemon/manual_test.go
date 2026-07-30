// SPDX-License-Identifier: MIT

package daemon

import (
	"errors"
	"os"
	"path"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/phil9922/backup-maker/internal/config"
	"github.com/phil9922/backup-maker/internal/localmirror"
	"github.com/phil9922/backup-maker/internal/statuspage"
	"github.com/phil9922/backup-maker/internal/webui"
)

// fakeManual stands in for the built-in documentation, which a test binary does
// not carry (the embedded tree is installed by the main package). Shaped like
// the real thing in the way that matters here: pages in subdirectories, a binary
// asset, and a file at the root.
func fakeManual() []webui.DocsFile {
	return []webui.DocsFile{
		{Name: "index.html", Data: []byte("<h1>backup-maker</h1>"), Page: true},
		{Name: "guide/1-install.html", Data: []byte("<h1>Installing</h1>"), Page: true},
		{Name: "guide/6-restoring.html", Data: []byte("<h1>Getting files back</h1>"), Page: true},
		{Name: "screenshots/dash.png", Data: []byte("\x89PNG\r\n\x1a\n")},
		{Name: "style.css", Data: []byte("body{}")},
	}
}

// withManual gives a page writer a manual to hand out. The id is read through a
// pointer so a test can put the writer on a different build mid-test.
func withManual(w *pageWriter, id *string, files func() []webui.DocsFile) {
	w.d.docsID = func() (string, error) { return *id, nil }
	w.d.docsBuild = func() ([]webui.DocsFile, error) { return files(), nil }
}

// manualDir is where the pages land on the destination, on disk.
func (w *pageWriter) manualDir() string {
	return filepath.Join(w.root, config.MachineDir("laptop"), manualDirName)
}

// manualWrites counts every write aimed at the manual, so "written once per
// build" can be asserted rather than inferred from the files being present.
func (w *pageWriter) manualWrites() int {
	prefix := path.Join(config.MachineDir("laptop"), manualDirName) + "/"
	n := 0
	for p, count := range w.fs.writes {
		if strings.HasPrefix(p, prefix) {
			n += count
		}
	}
	return n
}

func (w *pageWriter) pageBody(t *testing.T) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(w.root, statuspage.PathFor("laptop")))
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

// THE GUARANTEE: the manual is written once per build, not once a minute.
//
// It is two and a half megabytes, nearly all of it screenshots, and it changes
// only when the program does. The status page beside it earned a fifteenfold
// reduction in writes for being a few kilobytes; this arriving on every cycle
// would put all of that back and then some, on destinations that are commonly SD
// cards.
func TestTheManualIsWrittenOncePerBuildRatherThanEveryCycle(t *testing.T) {
	w := newPageWriter(t)
	id := "manual-of-0.1.16"
	withManual(w, &id, fakeManual)

	w.cycleAt(noon)
	first := w.manualWrites()
	if want := len(fakeManual()) + 1; first != want {
		t.Fatalf("the first cycle wrote %d manual files, want %d (every page plus the stamp)", first, want)
	}

	// Well past a recheck, so the destination is asked again and has to answer
	// with its own stamp rather than with anything remembered here.
	for i := 1; i <= 10; i++ {
		w.cycleAt(noon.Add(time.Duration(i) * time.Minute))
	}
	if got := w.manualWrites(); got != first {
		t.Errorf("ten more cycles took the manual writes from %d to %d; a manual that is "+
			"already there must not be written again", first, got)
	}
}

// THE GUARANTEE: a destination holding last version's manual is given this one.
//
// The direction that matters. A stale manual is not obviously stale — it looks
// exactly as authoritative as a current one, and it is read by somebody with no
// way to check it against anything. So the copy is keyed to the build that wrote
// it, and a daemon that finds a different one replaces it.
func TestANewBuildReplacesTheManualAlreadyOnTheDestination(t *testing.T) {
	root := t.TempDir()
	if err := localmirror.WriteMarkerAt(root, "card-uuid", "laptop"); err != nil {
		t.Fatal(err)
	}

	old := newPageWriterAt(t, root)
	before := "manual-of-0.1.16"
	withManual(old, &before, fakeManual)
	old.cycleAt(noon)
	if old.manualWrites() == 0 {
		t.Fatal("the first daemon wrote no manual at all")
	}

	// A restart on a newer build: a fresh daemon, the same destination, nothing
	// remembered in memory about what is on it.
	upgraded := newPageWriterAt(t, root)
	after := "manual-of-0.1.17"
	withManual(upgraded, &after, func() []webui.DocsFile {
		return append(fakeManual(), webui.DocsFile{
			Name: "guide/7-new-page.html", Data: []byte("<h1>New</h1>"), Page: true,
		})
	})
	upgraded.cycleAt(noon.Add(time.Hour))
	if got := upgraded.manualWrites(); got == 0 {
		t.Error("a newer build left the previous version's manual on the destination")
	}
	if _, err := os.Stat(filepath.Join(upgraded.manualDir(), "guide", "7-new-page.html")); err != nil {
		t.Errorf("the new build's pages are not on the destination: %v", err)
	}

	// And the same build, restarted, does not rewrite what it recognises.
	same := newPageWriterAt(t, root)
	withManual(same, &after, fakeManual)
	same.cycleAt(noon.Add(2 * time.Hour))
	if got := same.manualWrites(); got != 0 {
		t.Errorf("a restart on the same build rewrote the manual (%d writes); the stamp "+
			"on the destination is what should have prevented that", got)
	}
}

// THE GUARANTEE: the page links to the manual only where the manual actually is.
//
// The reader of this page is holding a drive whose computer will not start.
// Sending that person to a link that does not resolve is the small version of
// the one lie this program must never tell — and it is the version that is easy
// to ship, because the page is rendered from a model that knows nothing about
// which destination it is about to be written to.
func TestThePageOnlyPromisesAManualThatIsThere(t *testing.T) {
	w := newPageWriter(t)
	id := "manual-1"
	withManual(w, &id, fakeManual)
	w.cycleAt(noon)

	body := w.pageBody(t)
	if !strings.Contains(body, manualHref) {
		t.Fatalf("the page does not link to the manual sitting beside it; page was:\n%s", body)
	}
	// The link is relative to the page, so it is only right if the file is where
	// following it would land.
	if _, err := os.Stat(filepath.Join(w.root, config.MachineDir("laptop"), filepath.FromSlash(manualHref))); err != nil {
		t.Errorf("the page's own link does not resolve to a file: %v", err)
	}
}

// THE GUARANTEE, the other half: a destination that did not take the manual gets
// a page that does not mention one.
func TestAPageOnADestinationWithoutTheManualDoesNotMentionIt(t *testing.T) {
	w := newPageWriter(t)
	id := "manual-1"
	withManual(w, &id, fakeManual)
	// One page refused, so the manual is incomplete and never stamped.
	w.fs.failOn = path.Join(config.MachineDir("laptop"), manualDirName, "guide/1-install.html")

	w.cycleAt(noon)
	if body := w.pageBody(t); strings.Contains(body, manualHref) {
		t.Error("the manual could not be written, and the status page linked to it anyway")
	}
}

// THE GUARANTEE: gaining the manual rewrites the page, even when nothing else
// about the machine changed.
//
// Otherwise the link would wait for the next thing to happen on this computer —
// up to a heartbeat on an idle one, and the whole point is that it is there
// before anybody needs it.
func TestTheManualArrivingRewritesAnOtherwiseUnchangedPage(t *testing.T) {
	// The two intervals have to differ or this proves nothing: a manual that can
	// only arrive on a page-heartbeat cycle gets its link written either way, and
	// the fingerprint could be dropped with every test still passing. That is
	// exactly what happened — see manualRecheckEvery.
	if manualRecheckEvery >= statusHeartbeat {
		t.Fatalf("manualRecheckEvery (%s) is not inside statusHeartbeat (%s), so a page "+
			"rewritten when the manual arrives cannot be told from the heartbeat",
			manualRecheckEvery, statusHeartbeat)
	}

	w := newPageWriter(t)
	id := "manual-1"
	withManual(w, &id, fakeManual)
	w.fs.failOn = path.Join(config.MachineDir("laptop"), manualDirName, "index.html")

	w.cycleAt(noon)
	if got := w.pageWrites(); got != 1 {
		t.Fatalf("the page was written %d times on the first cycle, want 1", got)
	}
	if body := w.pageBody(t); strings.Contains(body, manualHref) {
		t.Fatal("this test proves nothing: the page linked to a manual that was refused")
	}

	w.fs.failOn = ""
	// Inside the page's own heartbeat, so the only thing that can earn this
	// write is the page having something new to say.
	w.cycleAt(noon.Add(manualRecheckEvery))
	if got := w.pageWrites(); got != 2 {
		t.Errorf("the manual arrived and the page was written %d times, want 2: it would "+
			"still be a page with no way to the manual beside it", got)
	}
	if body := w.pageBody(t); !strings.Contains(body, manualHref) {
		t.Error("the page was rewritten without gaining the link")
	}
}

// THE GUARANTEE: a manual that could not be produced at all is not promised.
//
// The build-carries-no-documentation path, which is what every test binary and
// any trimmed build looks like. It must leave nothing behind and, above all,
// must not leave a page pointing at a directory that was never created.
func TestAManualThatCannotBeBuiltIsNotPromised(t *testing.T) {
	w := newPageWriter(t)
	id := "manual-1"
	w.d.docsID = func() (string, error) { return id, nil }
	w.d.docsBuild = func() ([]webui.DocsFile, error) {
		return nil, errors.New("this build carries no documentation")
	}

	w.cycleAt(noon)
	if got := w.manualWrites(); got != 0 {
		t.Errorf("%d files were written for a manual that could not be built", got)
	}
	if body := w.pageBody(t); strings.Contains(body, manualHref) {
		t.Error("the page links to a manual this build cannot produce")
	}
}

// THE GUARANTEE: a manual that only half arrived is not recorded as present, and
// is written again.
//
// A stamp written first, or written regardless, would leave a destination
// holding a directory of some-of-the-pages that every later daemon recognises as
// complete — and the missing page is as likely to be "getting your files back"
// as any other.
func TestAHalfWrittenManualIsNotStampedAndIsTriedAgain(t *testing.T) {
	w := newPageWriter(t)
	id := "manual-1"
	withManual(w, &id, fakeManual)
	w.fs.failOn = path.Join(config.MachineDir("laptop"), manualDirName, "guide/6-restoring.html")

	w.cycleAt(noon)
	if _, err := os.Stat(filepath.Join(w.manualDir(), manualStampName)); err == nil {
		t.Error("a manual that could not be written completely was stamped as present")
	}

	w.fs.failOn = ""
	w.cycleAt(noon.Add(manualRecheckEvery))
	if _, err := os.Stat(filepath.Join(w.manualDir(), manualStampName)); err != nil {
		t.Errorf("the manual was not written again after the destination accepted writes: %v", err)
	}
	if _, err := os.Stat(filepath.Join(w.manualDir(), "guide", "6-restoring.html")); err != nil {
		t.Errorf("the page that had been refused is still missing: %v", err)
	}
}

// THE GUARANTEE: the manual is not handed two megabytes at a time to a
// destination that keeps refusing it.
func TestARefusedManualIsNotRetriedEveryCycle(t *testing.T) {
	w := newPageWriter(t)
	id := "manual-1"
	withManual(w, &id, fakeManual)
	w.fs.failOn = path.Join(config.MachineDir("laptop"), manualDirName, "index.html")

	w.cycleAt(noon)
	first := w.manualWrites()
	for at := time.Minute; at < manualRecheckEvery; at += time.Minute {
		w.cycleAt(noon.Add(at))
	}
	if got := w.manualWrites(); got != first {
		t.Errorf("a refused manual was retried within the recheck interval: %d writes became %d", first, got)
	}
	w.cycleAt(noon.Add(manualRecheckEvery))
	if got := w.manualWrites(); got <= first {
		t.Errorf("a refused manual was never retried at all: still %d writes", got)
	}
}

// THE GUARANTEE: nothing the manual writes can land outside this machine's own
// directory on the destination.
//
// The names come from this binary's embedded tree, so this cannot happen today.
// It is asserted because of what it would do if it ever could: path.Join
// resolves a ".." rather than refusing it, so one bad name writes into another
// computer's directory or over the destination's own index page.
func TestTheManualNeverWritesOutsideThisMachinesDirectory(t *testing.T) {
	w := newPageWriter(t)
	id := "manual-1"
	withManual(w, &id, func() []webui.DocsFile {
		return []webui.DocsFile{
			{Name: "../../escaped.html", Data: []byte("<h1>nope</h1>"), Page: true},
			{Name: "index.html", Data: []byte("<h1>manual</h1>"), Page: true},
		}
	})

	w.cycleAt(noon)
	for p := range w.fs.writes {
		if !strings.HasPrefix(p, config.MachineDir("laptop")+"/") && p != statuspage.FileName {
			t.Errorf("a write landed at %q, outside this machine's directory", p)
		}
	}
	if _, err := os.Stat(filepath.Join(w.root, "escaped.html")); err == nil {
		t.Error("a manual file with .. in its name escaped this machine's directory")
	}
	if _, err := os.Stat(filepath.Join(w.manualDir(), manualStampName)); err == nil {
		t.Error("a manual containing a refused name was stamped as complete")
	}
}

// THE GUARANTEE: storage we refuse to back up to is given no manual either.
//
// The manual itself discloses nothing — it is the same file as the public
// documentation. The rule is about the destination, not the contents: a user
// told a target is offline must not find this program's files appearing on it,
// and mayWrite is the one place that decision is made.
func TestUnrecognisedStorageIsGivenNoManual(t *testing.T) {
	w := newPageWriter(t)
	id := "manual-1"
	withManual(w, &id, fakeManual)
	// The drive was swapped for a different one: same mount point, different
	// identity marker.
	if err := localmirror.WriteMarkerAt(w.root, "some-other-drive", "laptop"); err != nil {
		t.Fatal(err)
	}

	w.cycleAt(noon)
	if got := w.manualWrites(); got != 0 {
		t.Errorf("%d manual files were written to storage this daemon refuses to back up to", got)
	}
	if _, err := os.Stat(w.manualDir()); err == nil {
		t.Error("the manual's directory was created on unrecognised storage")
	}
}

// THE GUARANTEE: the manual's directory cannot be mistaken for a folder's mirror.
//
// A folder's live mirror lands at <machine>/<label>, so an ordinary name here
// would be a name a folder label could collide with — and the mirror engine
// would then version every page away on its next pass as a file with no source,
// then be handed them all again by the next daemon start. Forever, on a card.
func TestTheManualsDirectoryCannotCollideWithAFoldersMirror(t *testing.T) {
	if !strings.HasPrefix(manualDirName, ".") {
		t.Errorf("manualDirName is %q: an ordinary name is one a folder label can be, "+
			"and the collision is a mirror pass deleting the manual", manualDirName)
	}
	here := path.Join(config.MachineDir("laptop"), manualDirName)
	for _, label := range []string{"manual", "Manual", "docs", "backup-maker-manual", "Documentation"} {
		if got := config.DestRoot("laptop", label); config.SameDest(got, here) {
			t.Errorf("a folder labelled %q mirrors to %q, which is where the manual goes", label, got)
		}
	}
}
