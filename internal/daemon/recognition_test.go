// SPDX-License-Identifier: MIT

package daemon

import (
	"bytes"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/phil9922/backup-maker/internal/config"
	"github.com/phil9922/backup-maker/internal/localmirror"
	"github.com/phil9922/backup-maker/internal/setup"
	"github.com/phil9922/backup-maker/internal/status"
	"github.com/phil9922/backup-maker/internal/statuspage"
)

// The phrase the refusal is logged with; counted rather than merely detected,
// because the point is that it is said once and not once a minute.
const refusalPhrase = "nothing is being written there"

// destWriter wires up the two things the daemon leaves at a destination root —
// the adoption manifest and the status page — against one fake destination
// directory, and runs both the way the daemon does.
type destWriter struct {
	d      *daemon
	target config.Target
	cfg    *config.Config
	logs   *bytes.Buffer
	root   string
}

func newDestWriter(t *testing.T, root, recordedUUID string) *destWriter {
	t.Helper()
	target := config.Target{Type: "drive", Name: "card", Path: root, Folders: []string{"dev"}}
	cfg := &config.Config{
		General: config.General{MachineName: "workstation"},
		Folders: []config.Folder{{ID: "dev", Label: "Development", Path: "/home/pk/Desktop/Development"}},
		Targets: []config.Target{target},
	}
	logs := &bytes.Buffer{}
	d := &daemon{
		log:   slog.New(slog.NewTextHandler(logs, nil)),
		state: &config.State{DriveTargetUUIDs: map[string]string{"card": recordedUUID}},
		cfg:   cfg,
	}
	d.statusPageBackends = []namedBackend{{
		name: target.Name, where: root, uuid: recordedUUID,
		backend: localmirror.NewLocalFS(root),
	}}
	return &destWriter{d: d, target: target, cfg: cfg, logs: logs, root: root}
}

// pass is one full round of everything the daemon writes to a destination root:
// the manifest on config apply, the status page on its minute timer.
func (w *destWriter) pass() {
	uuid := w.d.state.DriveTargetUUIDs[w.target.Name]
	w.d.refreshManifest(w.target, localmirror.NewLocalFS(w.root), w.cfg, uuid)
	w.d.writeStatusPages(status.Model{MachineName: w.cfg.General.MachineName}, time.Now())
}

func (w *destWriter) refusals() int {
	return strings.Count(w.logs.String(), refusalPhrase)
}

// entries lists what is actually on the destination — the assertion that
// matters is about files on a drive, not about a boolean somewhere in here.
func entries(t *testing.T, root string) []string {
	t.Helper()
	des, err := os.ReadDir(root)
	if err != nil {
		t.Fatalf("reading destination: %v", err)
	}
	out := make([]string, len(des))
	for i, de := range des {
		out[i] = de.Name()
	}
	sort.Strings(out)
	return out
}

func exists(t *testing.T, root, name string) bool {
	t.Helper()
	_, err := os.Stat(filepath.Join(root, name))
	return err == nil
}

// The manifest and the status page live inside the writing machine's own
// directory now, so a shared destination keeps one of each per computer rather
// than whichever wrote last. These name them for the machine these tests use.
var (
	ourManifest = setup.ManifestPathFor("workstation")
	ourStatus   = statuspage.PathFor("workstation")
	statusIndex = statuspage.FileName // the derived root page listing machines
)

// THE BUG: the mirror correctly refused to back up to a card whose identity
// marker no longer matched — and the daemon then wrote the manifest and the
// status page to it anyway. The manifest names this machine, its source folder
// paths and every destination it backs up to; the user had been told the target
// was offline and that nothing had been written.
func TestForeignStorageReceivesNothingAndIsReportedOnce(t *testing.T) {
	root := t.TempDir()
	if err := localmirror.WriteMarkerAt(root, "somebody-elses-uuid", "their-laptop"); err != nil {
		t.Fatal(err)
	}
	w := newDestWriter(t, root, "our-uuid")

	w.pass()
	w.pass()

	if got := entries(t, root); len(got) != 1 || got[0] != localmirror.MarkerName {
		t.Errorf("files landed on a stranger's drive: %v", got)
	}
	if exists(t, root, ourManifest) {
		t.Error("the manifest — machine name, source paths, every destination — was disclosed to unrecognised storage")
	}
	if exists(t, root, ourStatus) || exists(t, root, statusIndex) {
		t.Error("a status page was written to unrecognised storage")
	}
	if n := w.refusals(); n != 1 {
		t.Errorf("two passes logged %d refusals, want exactly 1\n%s", n, w.logs.String())
	}

	// The marker coming back means the real card is here again: writing resumes,
	// and a foreign drive turning up after that is news once more.
	if err := localmirror.WriteMarkerAt(root, "our-uuid", "workstation"); err != nil {
		t.Fatal(err)
	}
	w.pass()
	if !exists(t, root, ourManifest) || !exists(t, root, ourStatus) {
		t.Fatalf("the recognised card was not written to: %v", entries(t, root))
	}
	if err := localmirror.WriteMarkerAt(root, "somebody-elses-uuid", "their-laptop"); err != nil {
		t.Fatal(err)
	}
	w.pass()
	if n := w.refusals(); n != 2 {
		t.Errorf("a foreign drive appearing a second time logged %d refusals in total, want 2\n%s", n, w.logs.String())
	}
}

// The exact case seen on real hardware: the card was reformatted, so there is no
// marker at all — while a UUID for it is still recorded here. The mirror calls
// that offline; the manifest and the status page must call it the same thing.
func TestReformattedDestinationReceivesNothing(t *testing.T) {
	root := t.TempDir() // mounted and readable, and now blank
	w := newDestWriter(t, root, "our-uuid")

	w.pass()
	w.pass()

	if got := entries(t, root); len(got) != 0 {
		t.Errorf("a reformatted card received %v", got)
	}
	if n := w.refusals(); n != 1 {
		t.Errorf("two passes logged %d refusals, want exactly 1\n%s", n, w.logs.String())
	}
}

// A destination that simply is not plugged in is ordinary, not alarming: it must
// be written nothing and said nothing about.
func TestUnpluggedDestinationIsRefusedQuietly(t *testing.T) {
	root := filepath.Join(t.TempDir(), "not-mounted")
	w := newDestWriter(t, root, "our-uuid")

	w.pass()
	w.pass()

	if _, err := os.Stat(root); !os.IsNotExist(err) {
		t.Errorf("the mount point was brought into existence: %v", err)
	}
	if n := w.refusals(); n != 0 {
		t.Errorf("an absent drive was reported %d times as foreign storage\n%s", n, w.logs.String())
	}
}

// First run must still work: a brand-new empty drive has no marker until setup
// establishes one, and that is the path that makes adoption possible at all.
func TestFirstRunDestinationIsMarkedAndWritten(t *testing.T) {
	isolateState(t)
	root := t.TempDir() // a blank drive the user has just picked

	// What add-target/the wizard does before the daemon ever sees the target.
	if err := setup.EnsureTargetMarker(localmirror.NewLocalFS(root), "card", "workstation"); err != nil {
		t.Fatalf("establishing the target: %v", err)
	}
	state, err := config.LoadState()
	if err != nil {
		t.Fatalf("loading state: %v", err)
	}
	uuid := state.DriveTargetUUIDs["card"]
	if uuid == "" {
		t.Fatal("setup recorded no UUID for the new target")
	}
	w := newDestWriter(t, root, uuid)

	w.pass()

	if !exists(t, root, localmirror.MarkerName) {
		t.Error("the marker was not established on the new drive")
	}
	if !exists(t, root, ourManifest) {
		t.Fatal("a newly set-up drive got no manifest; adoption from that drive would be impossible")
	}
	if !exists(t, root, ourStatus) {
		t.Error("a newly set-up drive got no status page")
	}
	if !exists(t, root, statusIndex) {
		t.Error("a newly set-up drive got no status index at its root; every bookmark and symlink pointing there would 404")
	}
	m, err := setup.ReadManifestAt(root)
	if err != nil {
		t.Fatalf("reading the manifest back: %v", err)
	}
	if m.MachineName != "workstation" || len(m.Folders) != 1 {
		t.Errorf("manifest does not describe this machine: %+v", m)
	}
	if n := w.refusals(); n != 0 {
		t.Errorf("first-time setup was reported as foreign storage\n%s", w.logs.String())
	}
}

// The healthy case, unchanged: a recognised drive keeps getting both files on
// every pass.
func TestRecognizedDestinationKeepsGettingBothFiles(t *testing.T) {
	root := t.TempDir()
	if err := localmirror.WriteMarkerAt(root, "our-uuid", "workstation"); err != nil {
		t.Fatal(err)
	}
	w := newDestWriter(t, root, "our-uuid")

	w.pass()
	w.pass()

	for _, name := range []string{localmirror.MarkerName, ourManifest, ourStatus, statusIndex} {
		if !exists(t, root, name) {
			t.Errorf("%s missing from a recognised destination: %v", name, entries(t, root))
		}
	}
	if n := w.refusals(); n != 0 {
		t.Errorf("a healthy destination was refused\n%s", w.logs.String())
	}
}
