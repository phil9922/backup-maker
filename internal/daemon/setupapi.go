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
	cfg := d.currentCfg()
	user, pass := req.Username, req.Password
	if user == "" && pass == "" {
		user, pass = d.storedShareLogin(cfg, req.Machine)
	}
	return machines.StorageFor(ctx, cfg, req.Machine, user, pass, nil, nil, nil)
}

// storedShareLogin returns the credentials this computer already holds for a
// share host, or empty strings if it holds none.
//
// WHY THE DAEMON AND NOT THE machines PACKAGE: passwords live in state.json,
// which that package deliberately knows nothing about — it composes discover,
// browse and smbfs and takes credentials as arguments. The daemon is the piece
// that already holds both halves.
//
// Without this, browsing a destination this machine backs up to every day
// asked for its password again, because the request carried none and nothing
// looked. Combined with the destination not appearing in the list at all until
// a network scan found it, adding a second folder to an existing share meant
// scan, re-select, retype — every time.
func (d *daemon) storedShareLogin(cfg *config.Config, machineID string) (string, string) {
	if machineID == "" || machineID == machines.KindThis {
		return "", ""
	}
	creds := d.shareCredentials()
	for _, t := range cfg.Targets {
		if t.Type != "share" {
			continue
		}
		addr, err := machines.ShareAddr(t.URL)
		if err != nil || !strings.EqualFold(addr, machineID) {
			continue
		}
		if pass, ok := creds[t.Name]; ok {
			return t.Username, pass
		}
		// A guest share is configured with no password at all, and that is a
		// real answer: it is why the username is returned even when the
		// credential store has nothing for this target.
		return t.Username, ""
	}
	return "", ""
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

// createBackup runs the wizard's commit step. setup.CreateBackup resolves every
// chosen folder and validates every destination before writing anything, so a
// failure here leaves the configuration untouched rather than half-applied —
// with several folders in one request that is the difference between all of them
// being protected and the user being told about only the one that failed.
//
// The reported folder is the first of the selection; the names come back one per
// destination in the order they were sent, which the wizard maps back by index.
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
	folders := make([]setup.FolderRef, 0, len(req.Folders))
	for _, f := range req.Folders {
		folders = append(folders, setup.FolderRef{
			FolderID:    f.FolderID,
			Path:        f.Path,
			Label:       f.Label,
			ExtraIgnore: f.ExtraIgnore,
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
		Folders:      folders,
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
// Through updateState, so the flag is set on the state.json that is actually on
// disk and nothing else another command wrote while the daemon was running is
// carried away with it.
func (d *daemon) completeSetup() error {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.updateState(func(s *config.State) error {
		if s.SetupComplete {
			return config.ErrStateUnchanged
		}
		s.SetupComplete = true
		return nil
	})
}

// setupDone reports the persisted flag for the status model.
func (d *daemon) setupDone() bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.state != nil && d.state.SetupComplete
}
