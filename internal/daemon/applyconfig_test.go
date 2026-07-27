// SPDX-License-Identifier: MIT

package daemon

import (
	"bytes"
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/phil9922/backup-maker/internal/config"
	"github.com/phil9922/backup-maker/internal/localmirror"
	"github.com/phil9922/backup-maker/internal/setup"
)

// hangingDestination stands in for storage that has stopped answering: a NAS
// that was switched off, a laptop carried out of range, a share whose host is
// up but wedged. Opening it blocks until the test says otherwise, which is what
// smbfs's 5s dial and 60s operation timeouts look like from in here.
type hangingDestination struct {
	opened  chan struct{} // closed once the daemon has asked for the connection
	release chan struct{} // closed by the test to let the destination answer
	once    sync.Once
	root    string
}

func (h *hangingDestination) open(config.Target, map[string]string) (localmirror.Backend, time.Duration, bool, error) {
	h.once.Do(func() { close(h.opened) })
	<-h.release
	return localmirror.NewLocalFS(h.root), 5 * time.Second, false, nil
}

// THE REGRESSION THIS EXISTS FOR: applyConfig used to take the daemon's central
// lock and then, still holding it, dial every destination and rewrite each
// one's adoption manifest. The watchdog's whole definition of "this daemon is
// alive" is "d.mu can still be taken" (watchdog.go), so a few unreachable
// shares on one config reload could run down the watchdog's budget and have
// systemd kill a daemon that was working perfectly — a restart loop caused by
// an unplugged drive, which is the exact failure the watchdog is written to
// avoid.
//
// So: while a destination is refusing to open, the lock must still be free.
func TestConfigApplyLeavesTheCentralLockFreeWhileADestinationHangs(t *testing.T) {
	isolateState(t)
	root := t.TempDir()
	if err := localmirror.WriteMarkerAt(root, "card-uuid", "workstation"); err != nil {
		t.Fatal(err)
	}
	// Written to disk because applyConfig re-reads state.json — a setup command
	// may have recorded a UUID since the daemon started.
	state := &config.State{DriveTargetUUIDs: map[string]string{"card": "card-uuid"}}
	if err := state.Save(); err != nil {
		t.Fatalf("seeding state: %v", err)
	}
	cfg := &config.Config{
		General: config.General{MachineName: "workstation"},
		Targets: []config.Target{{Type: "drive", Name: "card", Path: root}},
	}
	logs := &bytes.Buffer{}
	d := &daemon{
		log:   slog.New(slog.NewTextHandler(logs, nil)),
		state: state,
		cfg:   cfg,
		marks: newSyncMarks(state),
	}
	dest := &hangingDestination{opened: make(chan struct{}), release: make(chan struct{}), root: root}
	d.newBackend = dest.open

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	applied := make(chan struct{})
	go func() {
		defer close(applied)
		d.applyConfig(ctx, cfg)
	}()

	select {
	case <-dest.opened:
	case <-time.After(10 * time.Second):
		t.Fatal("the config apply never reached the destination")
	}

	// The watchdog's liveness probe, asked repeatedly while the destination is
	// stuck. Once would be enough to catch the regression; several make it a
	// statement about the whole hang rather than one instant of it.
	for range 20 {
		if !d.lockResponsive() {
			t.Fatal("the central lock was held while a destination was being opened: " +
				"the watchdog would read this healthy daemon as wedged and systemd would kill it")
		}
		time.Sleep(5 * time.Millisecond)
	}

	close(dest.release)
	select {
	case <-applied:
	case <-time.After(10 * time.Second):
		t.Fatal("the config apply never finished")
	}

	// And the work itself still happened — moving it off the lock must not
	// quietly stop the manifest being refreshed.
	if _, err := os.Stat(filepath.Join(root, setup.ManifestName)); err != nil {
		t.Errorf("the adoption manifest was not refreshed during the apply: %v", err)
	}
}

// The other half of the same rule: d.state is what d.mu guards (see saveState),
// and applyConfig swaps it wholesale on every reload. It used to do that swap
// with no lock held at all, racing the tally's own flush timer. Run under
// -race, which is where this one earns its keep.
func TestConfigApplySwapsStateUnderTheLock(t *testing.T) {
	isolateState(t)
	state := &config.State{IPCToken: "live-token"}
	if err := state.Save(); err != nil {
		t.Fatalf("seeding state: %v", err)
	}
	logs := &bytes.Buffer{}
	cfg := &config.Config{General: config.General{MachineName: "workstation"}}
	d := &daemon{
		log:   slog.New(slog.NewTextHandler(logs, nil)),
		state: state,
		cfg:   cfg,
		marks: newSyncMarks(state),
	}
	d.tally = newTally(state, d.saveState)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	stop := make(chan struct{})
	var readers sync.WaitGroup
	for range 4 {
		readers.Add(1)
		go func() {
			defer readers.Done()
			for {
				select {
				case <-stop:
					return
				default:
					d.shareCredentials()
					d.tally.flush()
				}
			}
		}()
	}
	for range 10 {
		d.applyConfig(ctx, cfg)
	}
	close(stop)
	readers.Wait()

	// The live IPC token must survive every one of those reloads: rotating it
	// would log the user's browser out of a dashboard they left open.
	if got := d.shareCredentialsToken(); got != "live-token" {
		t.Errorf("the IPC token changed across config reloads: %q", got)
	}
}

// shareCredentialsToken reads the live token the same way everything else in
// the package reads d.state — under the lock.
func (d *daemon) shareCredentialsToken() string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.state.IPCToken
}
