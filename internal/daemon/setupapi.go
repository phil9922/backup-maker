// SPDX-License-Identifier: MIT

package daemon

import (
	"context"

	"github.com/phil9922/backup-maker/internal/config"
	"github.com/phil9922/backup-maker/internal/discover"
	"github.com/phil9922/backup-maker/internal/machines"
	"github.com/phil9922/backup-maker/internal/setup"
	"github.com/phil9922/backup-maker/internal/webui"
)

// listMachines returns computers that could hold backups. The LAN is only
// probed when scan is true — the promise that discovery never runs in the
// background is kept here, not just in the UI.
func (d *daemon) listMachines(ctx context.Context, scan bool) (any, error) {
	var scanner func(context.Context) ([]discover.Host, error)
	if scan {
		scanner = discover.Scan
	}
	return machines.List(ctx, d.currentCfg(), scanner)
}

func (d *daemon) machineStorage(ctx context.Context, req webui.StorageRequest) (any, error) {
	return machines.StorageFor(ctx, d.currentCfg(), req.Machine, req.Username, req.Password)
}

// createBackup runs the wizard's commit step. setup.CreateBackup validates
// every destination before writing anything, so a failure here leaves the
// configuration untouched rather than half-applied.
func (d *daemon) createBackup(req webui.BackupRequest) (any, error) {
	dests := make([]setup.Destination, 0, len(req.Destinations))
	for _, x := range req.Destinations {
		dests = append(dests, setup.Destination{
			ExistingTarget: x.ExistingTarget,
			Name:           x.Name,
			Path:           x.Path,
			URL:            x.URL,
			Username:       x.Username,
			Password:       x.Password,
			DeviceID:       x.DeviceID,
			MAC:            x.MAC,
			NoVerify:       x.NoVerify,
		})
	}
	var spec *setup.ArchiveSpec
	if req.Archive != nil {
		spec = &setup.ArchiveSpec{
			Name:              req.Archive.Name,
			Every:             req.Archive.Every,
			Keep:              req.Archive.Keep,
			Password:          req.Archive.Password,
			IncludeEverything: req.Archive.IncludeEverything,
		}
	}
	folder, targets, err := setup.CreateBackup(setup.BackupRequest{
		FolderID:     req.FolderID,
		Path:         req.Path,
		Label:        req.Label,
		ExtraIgnore:  req.ExtraIgnore,
		Mode:         req.Mode,
		Destinations: dests,
		Archive:      spec,
	})
	if err != nil {
		return nil, err
	}
	// Finishing the wizard is what "setup complete" means.
	_ = d.completeSetup()

	names := make([]string, 0, len(targets))
	for _, t := range targets {
		names = append(names, t.Name)
	}
	return map[string]any{
		"ok":           true,
		"folder":       folder,
		"destinations": names,
	}, nil
}

func (d *daemon) addArchive(req webui.ArchiveRequest) error {
	return setup.AddArchive(req.Name, req.Folders, req.Every, req.Target, req.Keep, req.Password)
}

// completeSetup records that the wizard was finished or deliberately skipped.
// It reloads state first so it never clobbers changes another command wrote
// while the daemon was running.
func (d *daemon) completeSetup() error {
	state, err := config.LoadState()
	if err != nil {
		return err
	}
	if state.SetupComplete {
		return nil
	}
	state.SetupComplete = true
	if err := state.Save(); err != nil {
		return err
	}
	d.mu.Lock()
	d.state.SetupComplete = true
	d.mu.Unlock()
	return nil
}

// setupDone reports the persisted flag for the status model.
func (d *daemon) setupDone() bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.state != nil && d.state.SetupComplete
}
