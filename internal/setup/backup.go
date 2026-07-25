// SPDX-License-Identifier: MIT

package setup

import (
	"errors"
	"fmt"

	"github.com/phil9922/backup-maker/internal/config"
	"github.com/phil9922/backup-maker/internal/smbfs"
)

// Destination is one place a backup should land, as chosen in the wizard.
// Exactly one of Path (a drive on this computer), URL (a network drive), or
// DeviceID (a paired machine) is set. ExistingTarget names a target that is
// already configured, in which case the others are ignored.
type Destination struct {
	ExistingTarget string `json:"existing_target,omitempty"`
	Name           string `json:"name,omitempty"`
	Path           string `json:"path,omitempty"`
	URL            string `json:"url,omitempty"`
	Username       string `json:"username,omitempty"`
	Password       string `json:"password,omitempty"`
	DeviceID       string `json:"device_id,omitempty"`
	MAC            string `json:"mac,omitempty"`
	NoVerify       bool   `json:"no_verify,omitempty"`
}

// Backup styles.
const (
	// ModeIncremental keeps a continuously updated mirror: a saved file is on
	// the destination within seconds, with ~30 days of previous versions.
	ModeIncremental = "incremental"
	// ModeTimed writes encrypted snapshots on a schedule and keeps no live
	// mirror. Nothing is copied between runs.
	ModeTimed = "timed"
)

// ArchiveSpec is the scheduled-snapshot half of a backup. Required for
// ModeTimed (which has no other protection), optional for ModeIncremental.
type ArchiveSpec struct {
	Name     string `json:"name"`
	Every    string `json:"every"`
	Keep     int    `json:"keep,omitempty"`
	Password string `json:"password"`
	// IncludeEverything seals the junk the mirror skips (node_modules, build
	// output, caches) into this snapshot only, leaving the live mirror lean.
	IncludeEverything bool `json:"include_everything,omitempty"`
}

// BackupRequest is one run of the wizard: protect this folder, send it to these
// destinations, in this style.
type BackupRequest struct {
	// FolderID protects a folder that is ALREADY set up, instead of adding a
	// new one. Without it there would be no way to give an existing folder a
	// second kind of backup — the duplicate-path guard rejects re-adding it,
	// so a folder mirrored to an SD card could never also be snapshotted.
	FolderID     string        `json:"folder_id,omitempty"`
	Path         string        `json:"path"`
	Label        string        `json:"label,omitempty"`
	ExtraIgnore  []string      `json:"extra_ignore,omitempty"`
	Mode         string        `json:"mode,omitempty"`
	Destinations []Destination `json:"destinations"`
	Archive      *ArchiveSpec  `json:"archive,omitempty"`
}

// CreateBackup adds one folder and wires it to every destination, all or
// nothing.
//
// Partial application is the worst possible outcome here: the user would be
// told they are protected while one destination silently does nothing. So every
// destination is validated and connection-tested BEFORE config.toml is written,
// and any failure aborts without saving.
//
// Note the one irreversible step: stamping a marker file on a drive or share.
// That is idempotent and harmless — an abandoned commit leaves a recognizable
// marker, never lost data.
func CreateBackup(req BackupRequest) (config.Folder, []config.Target, error) {
	cfg, err := config.Load()
	if err != nil {
		return config.Folder{}, nil, err
	}
	if len(req.Destinations) == 0 {
		return config.Folder{}, nil, errors.New("choose at least one place to back up to")
	}
	if req.Mode == "" {
		req.Mode = ModeIncremental
	}
	switch req.Mode {
	case ModeIncremental, ModeTimed:
	default:
		return config.Folder{}, nil, fmt.Errorf("unknown backup mode %q", req.Mode)
	}
	// A timed backup's only protection IS the schedule; creating one without
	// it would leave the folder listed as protected while nothing ever runs.
	if req.Mode == ModeTimed && req.Archive == nil {
		return config.Folder{}, nil, errors.New("a timed backup needs a schedule and a password")
	}

	var folder config.Folder
	if req.FolderID != "" {
		found := false
		for _, f := range cfg.Folders {
			if f.ID == req.FolderID {
				folder, found = f, true
				break
			}
		}
		if !found {
			return config.Folder{}, nil, fmt.Errorf("no folder with id %q", req.FolderID)
		}
	} else {
		var err error
		folder, err = AppendFolder(cfg, req.Path, req.Label, req.ExtraIgnore, false)
		if err != nil {
			return config.Folder{}, nil, err
		}
	}

	// Credentials discovered along the way; only persisted once everything
	// validates.
	pendingCreds := map[string]string{}
	var attached []config.Target

	for i, d := range req.Destinations {
		t, creds, err := resolveDestination(cfg, d, folder.ID, req.Mode)
		if err != nil {
			return config.Folder{}, nil, fmt.Errorf("destination %d of %d: %w", i+1, len(req.Destinations), err)
		}
		if creds != "" {
			pendingCreds[t.Name] = creds
		}
		attached = append(attached, t)
	}

	// The schedule is part of the same commit: for a timed backup it *is* the
	// protection, so it must not be possible to save the folder without it.
	archivePassword := ""
	if req.Archive != nil {
		name, err := appendArchive(cfg, folder, attached, *req.Archive)
		if err != nil {
			return config.Folder{}, nil, err
		}
		archivePassword = req.Archive.Password
		req.Archive.Name = name
	}

	if err := cfg.Save(); err != nil {
		return config.Folder{}, nil, err
	}

	if len(pendingCreds) > 0 || archivePassword != "" {
		state, err := config.LoadState()
		if err != nil {
			return config.Folder{}, nil, err
		}
		if state.ShareCredentials == nil {
			state.ShareCredentials = map[string]string{}
		}
		for name, pw := range pendingCreds {
			state.ShareCredentials[name] = pw
		}
		if archivePassword != "" {
			if state.ArchivePasswords == nil {
				state.ArchivePasswords = map[string]string{}
			}
			state.ArchivePasswords[req.Archive.Name] = archivePassword
		}
		if err := state.Save(); err != nil {
			return config.Folder{}, nil, err
		}
	}
	return folder, attached, nil
}

// appendArchive adds the snapshot schedule to cfg in memory and returns the
// name it was stored under.
func appendArchive(cfg *config.Config, folder config.Folder, dests []config.Target, spec ArchiveSpec) (string, error) {
	if spec.Password == "" {
		return "", errors.New("snapshots need a password — they are encrypted zips, and there is no way in without it")
	}
	if _, err := config.ParseEvery(spec.Every); err != nil {
		return "", err
	}
	name := spec.Name
	if name == "" {
		name = folder.Label
	}
	for _, a := range cfg.Archives {
		if a.Name == name {
			return "", fmt.Errorf("a schedule named %q already exists", name)
		}
	}

	// Snapshots are files written to storage, so a paired machine — which
	// receives a live mirror instead — can't hold them.
	dest := ""
	for _, t := range dests {
		if t.Type == "drive" || t.Type == "share" {
			dest = t.Name
			break
		}
	}
	if dest == "" {
		return "", errors.New("snapshots need a drive or network destination; a paired computer can't store them")
	}

	keep := spec.Keep
	if keep <= 0 {
		keep = config.DefaultArchiveKeep
	}
	cfg.Archives = append(cfg.Archives, config.Archive{
		Name: name, Folders: []string{folder.ID}, Every: spec.Every,
		Target: dest, Keep: keep, NoDefaultIgnores: spec.IncludeEverything,
	})
	return name, nil
}

// resolveDestination mutates cfg in memory: either associating the folder with
// an existing target, or creating a new one. It returns the resulting target
// and any share password that must be stored on success.
func resolveDestination(cfg *config.Config, d Destination, folderID, mode string) (config.Target, string, error) {
	timed := mode == ModeTimed

	if d.ExistingTarget != "" {
		for i := range cfg.Targets {
			if cfg.Targets[i].Name != d.ExistingTarget {
				continue
			}
			if timed {
				// A timed backup keeps no live copy, so the folder is
				// deliberately NOT attached for mirroring.
				return cfg.Targets[i], "", nil
			}
			if cfg.Targets[i].ArchivesOnly {
				// Promote a snapshot-only destination to also keep a live
				// copy. Its empty Folders list meant "mirror nothing" rather
				// than the usual "mirror everything", so it has to be scoped
				// explicitly — attachFolder would read the empty list as
				// "already covers every folder" and do nothing.
				cfg.Targets[i].ArchivesOnly = false
				cfg.Targets[i].Folders = []string{folderID}
				return cfg.Targets[i], "", nil
			}
			attachFolder(&cfg.Targets[i], folderID)
			return cfg.Targets[i], "", nil
		}
		return config.Target{}, "", fmt.Errorf("no target named %q", d.ExistingTarget)
	}

	switch {
	case d.Path != "":
		t, err := AppendDriveTarget(cfg, d.Path, d.Name)
		if err != nil {
			return config.Target{}, "", err
		}
		scopeNewTarget(cfg, t.Name, folderID, timed)
		return t, "", nil

	case d.URL != "":
		if _, _, share, _, err := smbfs.Parse(d.URL); err != nil {
			return config.Target{}, "", err
		} else if d.Name == "" {
			d.Name = share
		}
		if err := CheckNameFree(cfg, d.Name); err != nil {
			return config.Target{}, "", err
		}
		// Prove it works before promising the user anything.
		if err := smbfs.TestConnection(d.URL, d.Username, d.Password); err != nil {
			if d.Username == "" {
				return config.Target{}, "", fmt.Errorf("%w (this share may need a username and password)", err)
			}
			return config.Target{}, "", err
		}
		backend, err := smbfs.New(d.URL, d.Username, d.Password)
		if err != nil {
			return config.Target{}, "", err
		}
		defer backend.Close()
		if err := EnsureTargetMarker(backend, d.Name, cfg.General.MachineName); err != nil {
			return config.Target{}, "", err
		}
		t := config.Target{
			Type: "share", Name: d.Name, URL: d.URL,
			Username: d.Username, MAC: d.MAC,
			ArchivesOnly: timed, Folders: scopeFor(folderID, timed),
		}
		if d.NoVerify {
			f := false
			t.Verify = &f
		}
		cfg.Targets = append(cfg.Targets, t)
		return t, d.Password, nil

	case d.DeviceID != "":
		if timed {
			return config.Target{}, "", errors.New("a paired computer keeps a live mirror, so it can't be used for timed snapshots — pick a drive or network destination")
		}
		if d.Name == "" {
			d.Name = "machine-" + shortID(d.DeviceID)
		}
		if err := CheckNameFree(cfg, d.Name); err != nil {
			return config.Target{}, "", err
		}
		t := config.Target{
			Type: "device", Name: d.Name, DeviceID: d.DeviceID,
			MAC: d.MAC, Folders: []string{folderID},
		}
		cfg.Targets = append(cfg.Targets, t)
		return t, "", nil
	}
	return config.Target{}, "", errors.New("destination has no drive, network address, or device id")
}

// attachFolder scopes an existing target to include this folder. An empty
// Folders list already means "every folder", so it is deliberately left alone.
func attachFolder(t *config.Target, folderID string) {
	if len(t.Folders) == 0 {
		return
	}
	for _, id := range t.Folders {
		if id == folderID {
			return
		}
	}
	t.Folders = append(t.Folders, folderID)
}

// scopeFor returns the mirror scope for a newly created destination. A timed
// backup mirrors nothing, so the list stays empty and ArchivesOnly carries the
// meaning — an empty list on its own would mean "every folder".
func scopeFor(folderID string, timed bool) []string {
	if timed {
		return []string{}
	}
	return []string{folderID}
}

func scopeNewTarget(cfg *config.Config, name, folderID string, timed bool) {
	for i := range cfg.Targets {
		if cfg.Targets[i].Name == name {
			cfg.Targets[i].Folders = scopeFor(folderID, timed)
			cfg.Targets[i].ArchivesOnly = timed
			return
		}
	}
}

func shortID(id string) string {
	if i := len(id); i > 7 {
		return id[:7]
	}
	return id
}
