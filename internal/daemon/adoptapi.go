// SPDX-License-Identifier: MIT

package daemon

import (
	"errors"
	"fmt"

	"github.com/phil9922/backup-maker/internal/config"
	"github.com/phil9922/backup-maker/internal/setup"
	"github.com/phil9922/backup-maker/internal/smbfs"
	"github.com/phil9922/backup-maker/internal/webui"
)

// Adoption over HTTP: the browser flow behind "Restore this machine". All of
// these call straight into internal/setup — the same code the CLI adopt
// command runs — so the two flows can never drift apart.

func toAdoptSource(r webui.AdoptSourceRequest) setup.AdoptSource {
	return setup.AdoptSource{Path: r.Path, URL: r.URL, Username: r.Username, Password: r.Password}
}

// adoptScan lists attached drives carrying an adoption manifest.
func (d *daemon) adoptScan() (any, error) {
	list := setup.ScanForManifests()
	if list == nil {
		list = []setup.AdoptCandidate{} // JSON [] rather than null
	}
	return map[string]any{"candidates": list}, nil
}

// adoptInspect reads a destination's manifest and reports what adopting it
// would restore. Nothing is written.
func (d *daemon) adoptInspect(req webui.AdoptSourceRequest) (any, error) {
	return setup.InspectSource(toAdoptSource(req))
}

// adoptTestShare verifies share credentials with a real connection.
func (d *daemon) adoptTestShare(req webui.TestShareRequest) error {
	return smbfs.TestConnection(req.URL, req.Username, req.Password)
}

// adopt commits an adoption. The manifest is re-read from the source server-
// side — a client-supplied manifest is never trusted. The config watcher then
// applies the new configuration within seconds, like any other config change.
func (d *daemon) adopt(req webui.AdoptRequest) (any, error) {
	res, err := setup.AdoptFromSource(toAdoptSource(req.Source), setup.AdoptDecisions{
		ContinueAsMachine: req.ContinueAsMachine,
		NewMachineName:    req.NewMachineName,
		PathRemap:         req.PathRemap,
		SharePasswords:    req.SharePasswords,
		ArchivePasswords:  req.ArchivePasswords,
	})
	if err != nil {
		return nil, err
	}
	// Adopt set state.SetupComplete on disk; mirror it in memory so the
	// dashboard stops offering setup immediately, not at the next reload.
	d.mu.Lock()
	if d.state != nil {
		d.state.SetupComplete = true
	}
	d.mu.Unlock()
	return res, nil
}

// hasArchivePassword reports whether a snapshot job's zip password is stored,
// for the status model's "needs password" flag.
func (d *daemon) hasArchivePassword(name string) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.state == nil {
		return true // unknowable; don't cry wolf
	}
	_, ok := d.state.ArchivePasswords[name]
	return ok
}

// setArchivePassword stores the zip password for an existing snapshot job —
// the recovery path for a password skipped during adoption (or lost state).
func (d *daemon) setArchivePassword(name, password string) error {
	if password == "" {
		return errors.New("a password is required for snapshots")
	}
	cfg := d.currentCfg()
	found := false
	for _, a := range cfg.Archives {
		if a.Name == name {
			found = true
			break
		}
	}
	if !found {
		return fmt.Errorf("no scheduled snapshot named %q", name)
	}
	// Reload-modify-save so a concurrent CLI write is never clobbered.
	state, err := config.LoadState()
	if err != nil {
		return err
	}
	if state.ArchivePasswords == nil {
		state.ArchivePasswords = map[string]string{}
	}
	state.ArchivePasswords[name] = password
	if err := state.Save(); err != nil {
		return err
	}
	// The archive runner reads the daemon's in-memory state; update it too.
	d.mu.Lock()
	if d.state != nil {
		if d.state.ArchivePasswords == nil {
			d.state.ArchivePasswords = map[string]string{}
		}
		d.state.ArchivePasswords[name] = password
	}
	d.mu.Unlock()
	return nil
}
