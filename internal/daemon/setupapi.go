// SPDX-License-Identifier: MIT

package daemon

import (
	"context"
	"regexp"
	"strings"

	"github.com/phil9922/backup-maker/internal/browse"
	"github.com/phil9922/backup-maker/internal/config"
	"github.com/phil9922/backup-maker/internal/discover"
	"github.com/phil9922/backup-maker/internal/drivesetup"
	"github.com/phil9922/backup-maker/internal/machines"
	"github.com/phil9922/backup-maker/internal/setup"
	"github.com/phil9922/backup-maker/internal/version"
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
	return machines.StorageFor(ctx, d.currentCfg(), req.Machine, req.Username, req.Password, nil, nil, nil)
}

// unusableDrives reports attached storage that cannot hold backups yet.
//
// can_prepare says whether this computer will let the dashboard do anything
// about it. When it is false the page shows the commands to run instead, so
// the answer is never simply "no".
func (d *daemon) unusableDrives() (any, error) {
	list := browse.ListUnusable()
	if list == nil {
		list = []browse.Unusable{} // JSON [] rather than null
	}
	return map[string]any{
		"drives":      list,
		"can_prepare": drivesetup.Allowed(),
		// Shown whether or not the button is available, so the command that
		// would run as root is visible before it runs — and is there to paste
		// by hand when it cannot.
		"command_prefix": drivesetup.CommandPrefix(),
		"allow_command":  drivesetup.AllowCommand(),
	}, nil
}

// prepareDrive formats a blank drive and mounts it.
//
// The daemon does not do this itself and cannot: it runs as an ordinary user.
// It hands the request to the privileged subcommand, which re-checks every
// part of it — including the confirmation phrase, against the drive — before
// anything is written. Nothing decided in the browser survives the trip.
func (d *daemon) prepareDrive(ctx context.Context, req webui.PrepareDriveRequest) (any, error) {
	label := strings.TrimSpace(req.Label)
	if label == "" {
		label = "BACKUPS"
	}
	r := drivesetup.Request{
		Device:  req.Device,
		Mount:   req.Mount,
		Label:   label,
		Confirm: req.Confirm,
	}
	out, err := drivesetup.Run(ctx, r)
	if err != nil {
		return nil, err
	}
	d.log.Info("prepared a drive", "device", req.Device, "mount", req.Mount)
	return map[string]any{"output": out, "mount": req.Mount}, nil
}

// setupRecipe returns the commands that install backup-maker on another
// computer on the network.
//
// It deliberately runs nothing. A drive plugged into a machine that is not
// running backup-maker cannot be set up from here — no program can format a
// disk inside a computer it is not running on — and the useful thing to do
// about that is to say so and hand over the exact commands, not to invent a
// way of reaching in.
func (d *daemon) setupRecipe(machineID string) (any, error) {
	name := machineID
	for _, t := range d.currentCfg().Targets {
		if t.Type == "share" && strings.Contains(strings.ToLower(t.URL), strings.ToLower(machineID)) && t.Name != "" {
			name = t.Name
			break
		}
	}
	return map[string]any{
		"machine":   name,
		"version":   setupRecipeVersion(),
		"platforms": installRecipes(setupRecipeVersion()),
	}, nil
}

// setupRecipeVersion pins the instructions to the version this dashboard is
// running, so two machines on a network end up on the same build rather than
// whatever happened to be latest on the day each was set up.
//
// A build that is not an unmodified release pins nothing, and says so by
// returning "". A DIRTY BUILD IS THE TRAP HERE: it reports the version of the
// tag it was built from, so pinning to it would hand somebody the released
// 0.1.9 while this machine runs 0.1.9 plus changes — a Pi set up from those
// instructions would be missing exactly the features being used to write them.
func setupRecipeVersion() string {
	info := version.Get()
	return pinnedVersion(info.Version, info.Dirty)
}

// pinnedVersion is the decision on its own, so it can be tested without
// building six binaries to produce the states it has to get right.
func pinnedVersion(v string, dirty bool) string {
	if dirty {
		return ""
	}
	v = strings.TrimPrefix(v, "v")
	if !plainRelease.MatchString(v) {
		return ""
	}
	return v
}

// plainRelease matches a released version and nothing else — not "dev", not a
// pseudo-version, nothing that would build a download URL that 404s.
var plainRelease = regexp.MustCompile(`^\d+\.\d+\.\d+$`)

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
			CreateDir:      x.CreateDir,
			TakeOver:       x.TakeOver,
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
