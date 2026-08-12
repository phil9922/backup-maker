// SPDX-License-Identifier: MIT

package setup

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"

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
	// CreateDir makes the last component of Path if it is not there yet, for
	// "put backups in a folder on this drive". Only ever set by a control that
	// asked for a folder by name: the ordinary picker still requires the
	// directory to exist, so a typed path with a typo in it stays an error
	// rather than becoming a new empty directory on the wrong disk.
	CreateDir bool `json:"create_dir,omitempty"`
	// TakeOver confirms using a folder at the destination that already holds
	// backups filed under this machine's name. A decision made in front of the
	// conflict message and never inferred — see ClaimConflictError.
	TakeOver bool `json:"take_over,omitempty"`
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

// FolderRef names one folder in a run of the wizard. Exactly one of the two
// identifying fields is used: FolderID for a folder that is already set up,
// Path for one that is not.
type FolderRef struct {
	// FolderID protects a folder that is ALREADY set up, instead of adding a
	// new one. Without it there would be no way to give an existing folder a
	// second kind of backup — the duplicate-path guard rejects re-adding it,
	// so a folder mirrored to an SD card could never also be snapshotted.
	FolderID    string   `json:"folder_id,omitempty"`
	Path        string   `json:"path,omitempty"`
	Label       string   `json:"label,omitempty"`
	ExtraIgnore []string `json:"extra_ignore,omitempty"`
}

// BackupRequest is one run of the wizard: protect these folders, send them to
// these destinations, in this style.
type BackupRequest struct {
	// Folders is the whole selection when several folders are protected in one
	// pass. When it is set the single FolderID/Path pair below is ignored
	// entirely: the two forms describe the same thing, and merging them could
	// only protect something the caller never listed.
	Folders []FolderRef `json:"folders,omitempty"`
	// FolderID and Path are the older single-folder form of this request, still
	// sent by callers that only ever protect one folder at a time.
	FolderID     string        `json:"folder_id,omitempty"`
	Path         string        `json:"path"`
	Label        string        `json:"label,omitempty"`
	ExtraIgnore  []string      `json:"extra_ignore,omitempty"`
	Mode         string        `json:"mode,omitempty"`
	Destinations []Destination `json:"destinations"`
	Archive      *ArchiveSpec  `json:"archive,omitempty"`
}

// selection is the folders this request asks for, in the order they were sent.
func (req BackupRequest) selection() []FolderRef {
	if len(req.Folders) > 0 {
		return req.Folders
	}
	return []FolderRef{{
		FolderID: req.FolderID, Path: req.Path,
		Label: req.Label, ExtraIgnore: req.ExtraIgnore,
	}}
}

// CreateBackup adds the chosen folders and wires them to every destination, all
// or nothing.
//
// Partial application is the worst possible outcome here: the user would be
// told they are protected while one destination silently does nothing — or, with
// several folders in one request, while one of the folders was never saved. So
// every folder and every destination is resolved against an in-memory config and
// connection-tested BEFORE config.toml is written, and any failure aborts
// without saving.
//
// The folder returned is the FIRST of the selection: callers that show one
// folder back to the user (the dashboard's response) want that one, and the
// whole selection is in the config by the time this returns.
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

	folders, err := resolveFolders(cfg, req)
	if err != nil {
		return config.Folder{}, nil, err
	}

	// Which KIND of backup this is, recorded on each folder itself.
	//
	// The destination-side flag (ArchivesOnly, set by scopeFor below) only
	// governs destinations created here. Every destination already configured
	// with an empty folder list means "every folder", and would otherwise take
	// a folder created for snapshots and start mirroring it as well — which is
	// exactly how one request for a daily zip turned into a continuous copy on
	// every drive. Adding an incremental backup clears the flag, because that
	// is a person asking for the mirror this time.
	folderIDs := make([]string, 0, len(folders))
	for i := range folders {
		setSnapshotOnly(cfg, folders[i].ID, req.Mode == ModeTimed)
		folders[i].SnapshotOnly = req.Mode == ModeTimed
		folderIDs = append(folderIDs, folders[i].ID)
	}

	// Credentials discovered along the way; only persisted once everything
	// validates.
	pendingCreds := map[string]string{}
	var attached []config.Target

	for i, d := range req.Destinations {
		t, creds, err := resolveDestination(cfg, d, folderIDs, req.Mode)
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
		name, err := appendArchive(cfg, folders, attached, *req.Archive)
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
		if _, err := config.UpdateState(func(s *config.State) error {
			if s.ShareCredentials == nil {
				s.ShareCredentials = map[string]string{}
			}
			for name, pw := range pendingCreds {
				s.ShareCredentials[name] = pw
			}
			if archivePassword != "" {
				if s.ArchivePasswords == nil {
					s.ArchivePasswords = map[string]string{}
				}
				s.ArchivePasswords[req.Archive.Name] = archivePassword
			}
			return nil
		}); err != nil {
			return config.Folder{}, nil, err
		}
	}
	return folders[0], attached, nil
}

// resolveFolders turns the request's selection into folders on cfg, in memory
// and without saving, so the whole run can still be abandoned.
//
// ONE resolution path for both forms of the request and for every entry in it:
// an id names a folder that already exists, a path adds one.
func resolveFolders(cfg *config.Config, req BackupRequest) ([]config.Folder, error) {
	var chosen []config.Folder
	for _, ref := range req.selection() {
		if ref.FolderID == "" && strings.TrimSpace(ref.Path) == "" {
			return nil, errors.New("choose at least one folder to back up")
		}
		// The same folder twice means the caller sent a selection it did not
		// mean, so it is refused rather than quietly collapsed: the two entries
		// may disagree about the label or the excludes, and honouring one of
		// them silently is how somebody ends up with a backup they did not ask
		// for. AppendFolder catches a path that is already a folder; this
		// catches the pair the request created between them.
		if ref.Path != "" {
			abs, err := filepath.Abs(ExpandHome(ref.Path))
			if err != nil {
				return nil, err
			}
			for _, f := range chosen {
				if f.Path == abs {
					return nil, fmt.Errorf("the same folder was chosen twice: %s", abs)
				}
			}
		}
		var folder config.Folder
		if ref.FolderID != "" {
			found := false
			for _, f := range cfg.Folders {
				if f.ID == ref.FolderID {
					folder, found = f, true
					break
				}
			}
			if !found {
				return nil, fmt.Errorf("no folder with id %q", ref.FolderID)
			}
		} else {
			var err error
			folder, err = AppendFolder(cfg, ref.Path, ref.Label, ref.ExtraIgnore, false)
			if err != nil {
				return nil, err
			}
		}
		for _, f := range chosen {
			if f.ID == folder.ID {
				return nil, fmt.Errorf("the same folder was chosen twice: %s", folder.Label)
			}
		}
		chosen = append(chosen, folder)
	}
	return chosen, nil
}

// appendArchive adds the snapshot schedule to cfg in memory and returns the
// name it was stored under. ONE schedule covers the whole selection: a timed
// backup of three folders is three folders in one run, not three schedules
// racing each other onto the same destination.
func appendArchive(cfg *config.Config, folders []config.Folder, dests []config.Target, spec ArchiveSpec) (string, error) {
	if spec.Password == "" {
		return "", errors.New("snapshots need a password — they are encrypted zips, and there is no way in without it")
	}
	if _, err := config.ParseEvery(spec.Every); err != nil {
		return "", err
	}
	name := spec.Name
	if name == "" {
		name = folders[0].Label
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
	// Every chosen folder, named. An empty list here would mean EVERY folder on
	// the machine, which is why it is built from the selection and never left to
	// default.
	ids := make([]string, 0, len(folders))
	for _, f := range folders {
		ids = append(ids, f.ID)
	}
	cfg.Archives = append(cfg.Archives, config.Archive{
		Name: name, Folders: ids, Every: spec.Every,
		Target: dest, Keep: keep, NoDefaultIgnores: spec.IncludeEverything,
	})
	return name, nil
}

// resolveDestination mutates cfg in memory: either associating the folders with
// an existing target, or creating a new one. It returns the resulting target
// and any share password that must be stored on success.
func resolveDestination(cfg *config.Config, d Destination, folderIDs []string, mode string) (config.Target, string, error) {
	timed := mode == ModeTimed

	// EVERY Target below is created or re-scoped from this list, and an empty
	// Folders list on a mirroring target means EVERY folder. The selection is
	// checked long before this, so an empty list here is a bug upstream — this
	// is the last place that can still refuse it instead of widening a
	// destination to the whole machine.
	if len(folderIDs) == 0 {
		return config.Target{}, "", errors.New("no folders were chosen for this destination")
	}

	if d.ExistingTarget != "" {
		for i := range cfg.Targets {
			if cfg.Targets[i].Name != d.ExistingTarget {
				continue
			}
			if timed {
				// A timed backup keeps no live copy, so the folders are
				// deliberately NOT attached for mirroring.
				return cfg.Targets[i], "", nil
			}
			if cfg.Targets[i].ArchivesOnly {
				// Promote a snapshot-only destination to also keep a live
				// copy. Its empty Folders list meant "mirror nothing" rather
				// than the usual "mirror everything", so it has to be scoped
				// explicitly — attachFolders would read the empty list as
				// "already covers every folder" and do nothing.
				cfg.Targets[i].ArchivesOnly = false
				cfg.Targets[i].Folders = append([]string(nil), folderIDs...)
				return cfg.Targets[i], "", nil
			}
			attachFolders(&cfg.Targets[i], folderIDs)
			return cfg.Targets[i], "", nil
		}
		return config.Target{}, "", fmt.Errorf("no target named %q", d.ExistingTarget)
	}

	switch {
	case d.Path != "":
		t, err := AppendDriveTargetIn(cfg, d.Path, d.Name, d.CreateDir, d.TakeOver)
		if err != nil {
			return config.Target{}, "", err
		}
		scopeNewTarget(cfg, t.Name, folderIDs, timed)
		return t, "", nil

	case d.URL != "":
		if _, _, _, _, err := smbfs.Parse(d.URL); err != nil {
			return config.Target{}, "", err
		}
		name, nerr := TargetName(cfg, d.Name, DefaultShareTargetName(d.URL))
		if nerr != nil {
			return config.Target{}, "", nerr
		}
		d.Name = name
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
		if err := EnsureTargetMarkerAs(backend, d.Name, cfg.General.MachineName, d.TakeOver); err != nil {
			return config.Target{}, "", locate(err, "share", d.URL)
		}
		t := config.Target{
			Type: "share", Name: d.Name, URL: d.URL,
			Username: d.Username, MAC: d.MAC,
			ArchivesOnly: timed, Folders: scopeFor(folderIDs, timed),
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
		// Same rule as the CLI's add-target device, so a pasted ID is accepted
		// or refused identically wherever it was typed.
		deviceID, err := NormalizeDeviceID(d.DeviceID)
		if err != nil {
			return config.Target{}, "", err
		}
		// Already paired with this machine: send this folder there too, rather
		// than standing up a second target for the same computer (which the CLI
		// refuses outright — here there is an obviously better answer).
		for i := range cfg.Targets {
			if cfg.Targets[i].Type == "device" && cfg.Targets[i].DeviceID == deviceID {
				attachFolders(&cfg.Targets[i], folderIDs)
				return cfg.Targets[i], "", nil
			}
		}
		if d.Name == "" {
			d.Name = "machine-" + shortID(deviceID)
		}
		if err := CheckNameFree(cfg, d.Name); err != nil {
			return config.Target{}, "", err
		}
		t := config.Target{
			Type: "device", Name: d.Name, DeviceID: deviceID,
			MAC: d.MAC, Folders: append([]string(nil), folderIDs...),
		}
		cfg.Targets = append(cfg.Targets, t)
		return t, "", nil
	}
	return config.Target{}, "", errors.New("destination has no drive, network address, or device id")
}

// attachFolders EXTENDS an existing target's scope to include these folders.
//
// Extend, never replace: the ids already there are folders somebody asked this
// destination to keep, and dropping one would stop backing up a folder that was
// not part of this request. An empty Folders list already means "every folder",
// so it is deliberately left alone — narrowing it to the selection would stop
// every other folder from being copied here.
func attachFolders(t *config.Target, folderIDs []string) {
	if len(t.Folders) == 0 {
		return
	}
	for _, folderID := range folderIDs {
		known := false
		for _, id := range t.Folders {
			if id == folderID {
				known = true
				break
			}
		}
		if !known {
			t.Folders = append(t.Folders, folderID)
		}
	}
}

// setSnapshotOnly records on the folder itself whether it is here for
// scheduled snapshots alone. Kept beside scopeFor because the two answer the
// same question from the two ends — the folder's and the destination's — and
// only having the destination's end is what let a timed backup start
// mirroring.
func setSnapshotOnly(cfg *config.Config, folderID string, timed bool) {
	for i := range cfg.Folders {
		if cfg.Folders[i].ID == folderID {
			cfg.Folders[i].SnapshotOnly = timed
			return
		}
	}
}

// scopeFor returns the mirror scope for a newly created destination: exactly
// the folders that were chosen, and nothing else. A timed backup mirrors
// nothing, so the list stays empty and ArchivesOnly carries the meaning — an
// empty list on its own would mean "every folder".
func scopeFor(folderIDs []string, timed bool) []string {
	if timed {
		return []string{}
	}
	return append([]string(nil), folderIDs...)
}

func scopeNewTarget(cfg *config.Config, name string, folderIDs []string, timed bool) {
	for i := range cfg.Targets {
		if cfg.Targets[i].Name == name {
			cfg.Targets[i].Folders = scopeFor(folderIDs, timed)
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
