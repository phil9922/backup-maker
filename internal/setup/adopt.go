// SPDX-License-Identifier: MIT

package setup

import (
	"errors"
	"fmt"

	"github.com/phil9922/backup-maker/internal/config"
)

// AdoptDecisions carries the choices a fresh install makes when adopting an
// existing destination's manifest. The CLI and the dashboard both gather these
// (interactively / from a form) and hand them to Adopt, which owns the actual
// config.toml + state.json construction.
type AdoptDecisions struct {
	// ContinueAsMachine keeps the manifest's machine name AND every folder ID
	// verbatim, so backups keep landing in the same destination subtree
	// (sanitize(machine)/sanitize(label)) and any paired device stays paired.
	// When false, adoption starts as a NEW machine: this host's name (or
	// NewMachineName) and freshly minted folder IDs, i.e. a parallel copy.
	ContinueAsMachine bool
	// NewMachineName names the machine when not continuing as the old one.
	// Empty falls back to this host's name (config.New's default).
	NewMachineName string
	// PathRemap maps a manifest folder ID to a new absolute source path on this
	// machine (different username, different OS layout). A missing or empty
	// entry keeps the manifest's stored path.
	PathRemap map[string]string
	// SharePasswords maps a share target's name to its SMB password.
	SharePasswords map[string]string
	// ArchivePasswords maps an archive's name to its zip password.
	ArchivePasswords map[string]string
}

// AdoptResult summarizes what adoption created and what still needs a hand.
type AdoptResult struct {
	MachineName string
	Folders     int
	Targets     int
	Archives    int
	// MissingUUIDs names drive/share targets the manifest had no UUID for: the
	// daemon can't recognize them until they are re-added. Normally empty.
	MissingUUIDs []string
	// SharesNeedingPassword names share targets left without a stored password;
	// they stay idle until `backup-maker set-password <name>` is run.
	SharesNeedingPassword []string
}

// AdoptAllowed reports whether this machine may adopt: either no config file
// exists yet, or one exists (backup-maker init creates it before the daemon
// can run) but nothing is configured in it. Once a single folder or target
// exists, adoption is refused — it is a fresh-machine operation, not a merge.
func AdoptAllowed() error {
	if !config.Exists() {
		return nil
	}
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	if len(cfg.Folders) > 0 || len(cfg.Targets) > 0 {
		return errors.New("this machine already backs something up; adopt is for a fresh install")
	}
	return nil
}

// Adopt reconstructs this machine's configuration from a destination manifest.
// It refuses to run when anything is already configured — adoption is a
// fresh-install operation, not a merge.
func Adopt(m *Manifest, d AdoptDecisions) (*AdoptResult, error) {
	if m == nil {
		return nil, errors.New("no manifest to adopt")
	}
	if err := AdoptAllowed(); err != nil {
		return nil, err
	}

	// Start from the existing (empty) config when init already created one:
	// the daemon may be live on its dashboard port, and adoption must not
	// change general settings out from under it — only the machine name is
	// adoption's to decide.
	cfg := config.New()
	if config.Exists() {
		existing, err := config.Load()
		if err != nil {
			return nil, err
		}
		cfg = existing
	}
	switch {
	case d.ContinueAsMachine:
		if m.MachineName == "" {
			return nil, errors.New("manifest has no machine name to continue as")
		}
		cfg.General.MachineName = m.MachineName
	case d.NewMachineName != "":
		cfg.General.MachineName = d.NewMachineName
	}

	// Adopt the source machine's non-secret defaults (ignore list, versioning
	// horizon, reclaim headroom) so folders behave identically. Guard against a
	// manifest written before Defaults was populated: keep this host's defaults
	// rather than blanking them.
	if len(m.Defaults.Ignore) > 0 || m.Defaults.VersioningMaxAgeDays > 0 {
		cfg.Defaults = m.Defaults
	}

	// Folder identity: preserve IDs verbatim when continuing as the old machine
	// (destination subtree and device pairing depend on them); mint fresh IDs
	// for a new-machine parallel copy and remap every reference to match.
	idFor := make(map[string]string, len(m.Folders))
	for _, f := range m.Folders {
		if d.ContinueAsMachine {
			idFor[f.ID] = f.ID
		} else {
			idFor[f.ID] = config.NewFolderID()
		}
	}
	for _, f := range m.Folders {
		nf := f
		nf.ID = idFor[f.ID]
		if p := d.PathRemap[f.ID]; p != "" {
			nf.Path = p
		}
		cfg.Folders = append(cfg.Folders, nf)
	}

	state, err := config.LoadState()
	if err != nil {
		return nil, err
	}
	if state.DriveTargetUUIDs == nil {
		state.DriveTargetUUIDs = map[string]string{}
	}
	if state.ShareCredentials == nil {
		state.ShareCredentials = map[string]string{}
	}
	if state.ArchivePasswords == nil {
		state.ArchivePasswords = map[string]string{}
	}

	var missingUUID, needPass []string
	for _, mt := range m.Targets {
		t := mt.Target
		t.Folders = remapFolderIDs(t.Folders, idFor)
		cfg.Targets = append(cfg.Targets, t)

		if t.Type == "drive" || t.Type == "share" {
			if mt.UUID != "" {
				state.DriveTargetUUIDs[t.Name] = mt.UUID
			} else {
				missingUUID = append(missingUUID, t.Name)
			}
		}
		if t.Type == "share" {
			// Presence in the map is what counts, not non-emptiness: a guest
			// share's working password IS the empty string. Absent = the user
			// skipped it; the target stays idle until set-password.
			if pw, ok := d.SharePasswords[t.Name]; ok {
				state.ShareCredentials[t.Name] = pw
			} else {
				needPass = append(needPass, t.Name)
			}
		}
	}

	for _, a := range m.Archives {
		na := a
		na.Folders = remapFolderIDs(a.Folders, idFor)
		cfg.Archives = append(cfg.Archives, na)
		if pw := d.ArchivePasswords[a.Name]; pw != "" {
			state.ArchivePasswords[a.Name] = pw
		}
	}

	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("adopted configuration is invalid: %w", err)
	}

	state.SetupComplete = true
	if err := cfg.Save(); err != nil {
		return nil, err
	}
	if err := state.Save(); err != nil {
		return nil, err
	}

	return &AdoptResult{
		MachineName:           cfg.General.MachineName,
		Folders:               len(cfg.Folders),
		Targets:               len(cfg.Targets),
		Archives:              len(cfg.Archives),
		MissingUUIDs:          missingUUID,
		SharesNeedingPassword: needPass,
	}, nil
}

// remapFolderIDs rewrites a list of folder-ID references through idFor. An ID
// with no mapping is kept as-is so config validation surfaces it rather than
// silently dropping the reference.
func remapFolderIDs(ids []string, idFor map[string]string) []string {
	if len(ids) == 0 {
		return ids
	}
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		if n, ok := idFor[id]; ok {
			out = append(out, n)
		} else {
			out = append(out, id)
		}
	}
	return out
}
