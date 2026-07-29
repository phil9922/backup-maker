// SPDX-License-Identifier: MIT

package daemon

import (
	"bytes"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/phil9922/backup-maker/internal/config"
	"github.com/phil9922/backup-maker/internal/localmirror"
	"github.com/phil9922/backup-maker/internal/setup"
	"github.com/phil9922/backup-maker/internal/status"
	"github.com/phil9922/backup-maker/internal/statuspage"
)

// machineWriter is one computer writing to a shared destination: its own
// config, its own install id, its own view of the world.
type machineWriter struct {
	d    *daemon
	cfg  *config.Config
	root string
	logs *bytes.Buffer
}

func newMachineWriter(t *testing.T, root, machine, installID, uuid string) *machineWriter {
	t.Helper()
	target := config.Target{Type: "drive", Name: "card", Path: root, Folders: []string{}}
	cfg := &config.Config{
		General: config.General{MachineName: machine},
		Folders: []config.Folder{{ID: machine + "-dev", Label: "Development", Path: "/home/somebody/Development"}},
		Targets: []config.Target{target},
	}
	logs := &bytes.Buffer{}
	d := &daemon{
		log: slog.New(slog.NewTextHandler(logs, nil)),
		state: &config.State{
			InstallID:        installID,
			DriveTargetUUIDs: map[string]string{"card": uuid},
		},
		cfg: cfg,
	}
	d.statusPageBackends = []namedBackend{{
		name: "card", where: root, uuid: uuid, backend: localmirror.NewLocalFS(root),
	}}
	return &machineWriter{d: d, cfg: cfg, root: root, logs: logs}
}

func (w *machineWriter) pass() {
	uuid := w.d.state.DriveTargetUUIDs["card"]
	w.d.refreshManifest(w.cfg.Targets[0], localmirror.NewLocalFS(w.root), w.cfg, uuid)
	w.d.writeStatusPages(status.Model{MachineName: w.cfg.General.MachineName}, time.Now())
}

// THE BUG: the status page and the manifest were written at fixed names at the
// destination root. Two computers sharing a drive therefore overwrote each
// other's on every cycle — a minute apart, for ever — so the page beside the
// backups reported one machine and silently omitted the other, and adopting the
// drive could only rebuild whichever wrote last.
func TestTwoMachinesDoNotOverwriteEachOthersStatusPage(t *testing.T) {
	root := t.TempDir()
	if err := localmirror.WriteMarkerAt(root, "card-uuid", "laptop"); err != nil {
		t.Fatal(err)
	}
	laptop := newMachineWriter(t, root, "laptop", "install-laptop", "card-uuid")
	pi := newMachineWriter(t, root, "attic-pi", "install-pi", "card-uuid")

	// Interleaved, the way two daemons on one drive actually run.
	laptop.pass()
	pi.pass()
	laptop.pass()
	pi.pass()

	for _, machine := range []string{"laptop", "attic-pi"} {
		if !exists(t, root, statuspage.PathFor(machine)) {
			t.Errorf("%s has no status page on the shared drive", machine)
		}
		if !exists(t, root, setup.ManifestPathFor(machine)) {
			t.Errorf("%s has no manifest on the shared drive; it could not be restored from it", machine)
		}
	}

	// Each page describes its own machine, not the other's.
	for _, machine := range []string{"laptop", "attic-pi"} {
		page, err := os.ReadFile(filepath.Join(root, statuspage.PathFor(machine)))
		if err != nil {
			t.Fatalf("reading %s's page: %v", machine, err)
		}
		if !strings.Contains(string(page), machine) {
			t.Errorf("%s's status page does not name %s", machine, machine)
		}
	}

	// And the root page — the one every bookmark and symlink already points at
	// — lists both rather than being one machine's report.
	index, err := os.ReadFile(filepath.Join(root, statuspage.FileName))
	if err != nil {
		t.Fatalf("no index at the destination root: %v", err)
	}
	for _, machine := range []string{"laptop", "attic-pi"} {
		if !strings.Contains(string(index), machine) {
			t.Errorf("the root status page does not mention %s:\n%s", machine, index)
		}
	}

	// Both machines are adoptable from this drive, by name.
	found, err := setup.ListManifestsAt(root)
	if err != nil {
		t.Fatalf("ListManifestsAt: %v", err)
	}
	if len(found) != 2 {
		t.Fatalf("the shared drive offers %d machines to adopt, want 2: %+v", len(found), found)
	}
}

// The claim is what stops two computers that happen to share a machine name
// from writing into one directory. Nothing this machine leaves at a destination
// — manifest or status page — may land in a directory another install holds.
func TestNoManifestOrStatusPageLandsInAnotherComputersDirectory(t *testing.T) {
	root := t.TempDir()
	if err := localmirror.WriteMarkerAt(root, "card-uuid", "ubuntu"); err != nil {
		t.Fatal(err)
	}
	// Two fresh installs of the same distro image: both called "ubuntu".
	first := newMachineWriter(t, root, "ubuntu", "install-first", "card-uuid")
	second := newMachineWriter(t, root, "ubuntu", "install-second", "card-uuid")

	first.pass()
	firstManifest, err := os.ReadFile(filepath.Join(root, setup.ManifestPathFor("ubuntu")))
	if err != nil {
		t.Fatalf("the first machine wrote no manifest: %v", err)
	}

	second.pass()
	second.pass()

	after, err := os.ReadFile(filepath.Join(root, setup.ManifestPathFor("ubuntu")))
	if err != nil {
		t.Fatalf("reading the manifest back: %v", err)
	}
	if !bytes.Equal(firstManifest, after) {
		t.Error("the second computer overwrote the first's manifest in a directory it does not own")
	}
	if !strings.Contains(second.logs.String(), "another computer is already backing up") {
		t.Errorf("the second computer was not told why it is writing nothing:\n%s", second.logs.String())
	}
	// And it is said once, not once a minute for ever.
	if n := strings.Count(second.logs.String(), "another computer is already backing up"); n != 1 {
		t.Errorf("the refusal was logged %d times over two passes, want 1", n)
	}

	// The first machine is unaffected and keeps working.
	first.pass()
	if !exists(t, root, statuspage.PathFor("ubuntu")) {
		t.Error("the machine that owns the directory lost its status page")
	}
}

// The upgrade path, and the one that must not become a refusal: a destination
// that has been in service since before claims existed has no claim file on it.
// The daemon takes it rather than stopping.
func TestADestinationWithNoClaimIsAdoptedRatherThanRefused(t *testing.T) {
	root := t.TempDir()
	if err := localmirror.WriteMarkerAt(root, "card-uuid", "my-laptop"); err != nil {
		t.Fatal(err)
	}
	// A machine directory full of backups and no claim — exactly what a drive
	// looks like the moment the binary is replaced.
	if err := os.MkdirAll(filepath.Join(root, "my-laptop", "Development"), 0o755); err != nil {
		t.Fatal(err)
	}

	w := newMachineWriter(t, root, "my-laptop", "install-laptop", "card-uuid")
	w.pass()

	if !exists(t, root, statuspage.PathFor("my-laptop")) {
		t.Fatal("an upgrade stopped a working backup writing its status page")
	}
	if !exists(t, root, filepath.Join("my-laptop", localmirror.ClaimName)) {
		t.Error("the directory was used but never claimed, so the next machine along could take it")
	}
	if strings.Contains(w.logs.String(), "another computer is already backing up") {
		t.Errorf("a machine's own pre-upgrade directory was reported as somebody else's:\n%s", w.logs.String())
	}
}

// A destination can be a folder on a drive, and that folder keeps an index page
// of its own. A machine writing at the drive root must not list that folder as
// though it were a computer — the row would link to another index and name a
// computer that does not exist.
func TestANestedDestinationIsNotListedAsAComputer(t *testing.T) {
	root := t.TempDir()
	if err := localmirror.WriteMarkerAt(root, "card-uuid", "laptop"); err != nil {
		t.Fatal(err)
	}
	// A second destination living in a folder on the same drive, with the page
	// and marker it would really have.
	nested := filepath.Join(root, "Backups")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := localmirror.WriteMarkerAt(nested, "other-uuid", "subbox"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(nested, statuspage.FileName), []byte("<html>"), 0o644); err != nil {
		t.Fatal(err)
	}

	newMachineWriter(t, root, "laptop", "install-laptop", "card-uuid").pass()

	index, err := os.ReadFile(filepath.Join(root, statuspage.FileName))
	if err != nil {
		t.Fatalf("no index at the destination root: %v", err)
	}
	// It is NAMED — otherwise somebody picking up this drive never discovers
	// the backups in that folder — but not as a computer, which would link to
	// another index and claim a machine that does not exist. Matched on the
	// table cell rather than the bare word: the page's own heading is "Backups
	// on this storage", which would pass for entirely the wrong reason.
	if strings.Contains(string(index), `<td><a href="Backups/`) {
		t.Errorf("a nested destination folder was listed as a computer:\n%s", index)
	}
	if !strings.Contains(string(index), `<a href="Backups/`) {
		t.Errorf("the nested destination is unreachable from the page beside it:\n%s", index)
	}
	if !strings.Contains(string(index), "laptop") {
		t.Errorf("the real machine is missing from the index:\n%s", index)
	}
}
