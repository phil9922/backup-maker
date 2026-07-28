// SPDX-License-Identifier: MIT

package setup

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/phil9922/backup-maker/internal/config"
	"github.com/phil9922/backup-maker/internal/localmirror"
)

// CheckNameFree reports whether a target name is still available.
func CheckNameFree(cfg *config.Config, name string) error {
	for _, t := range cfg.Targets {
		if t.Name == name {
			return fmt.Errorf("a target named %q already exists", name)
		}
	}
	return nil
}

// AddDriveTarget adds storage attached to this computer as a backup target,
// stamping it with a marker file so different storage appearing at the same
// mount point is refused later.
func AddDriveTarget(path, name string) (config.Target, error) {
	return AddDriveTargetIn(path, name, false, false)
}

// AddDriveTargetIn is AddDriveTarget with the folder-creation and name-takeover
// decisions the CLI's flags carry.
func AddDriveTargetIn(path, name string, create, takeOver bool) (config.Target, error) {
	cfg, err := config.Load()
	if err != nil {
		return config.Target{}, err
	}
	t, err := AppendDriveTargetIn(cfg, path, name, create, takeOver)
	if err != nil {
		return config.Target{}, err
	}
	if err := cfg.Save(); err != nil {
		return config.Target{}, err
	}
	return t, nil
}

// AppendDriveTarget validates and stamps the drive, then adds it to cfg in
// memory without saving. The marker write is a real side effect on the drive
// and cannot be rolled back — but it is idempotent, so a later abandoned
// commit leaves only a recognizable marker behind, never data loss.
func AppendDriveTarget(cfg *config.Config, path, name string) (config.Target, error) {
	return AppendDriveTargetIn(cfg, path, name, false, false)
}

// AppendDriveTargetIn is AppendDriveTarget for a destination that may be a
// folder on a drive rather than the whole drive.
//
// A DESTINATION HAS ALWAYS BEEN "ANY DIRECTORY" — nothing here assumes a mount
// point — but until now there was no way to say "make me one". create adds
// that, and makes EXACTLY ONE level: os.Mkdir, never MkdirAll. A mistyped
// "/mnt/backupss" must stay an error, because a typo that silently succeeds
// produces a destination that looks like it is working while the backups go
// somewhere nobody will think to look.
//
// takeOver confirms a folder that already holds backups under this machine's
// name; see EnsureTargetMarkerAs.
func AppendDriveTargetIn(cfg *config.Config, path, name string, create, takeOver bool) (config.Target, error) {
	root, err := filepath.Abs(ExpandHome(path))
	if err != nil {
		return config.Target{}, err
	}
	if create {
		if err := ValidateFolderName(filepath.Base(root)); err != nil {
			return config.Target{}, err
		}
		// Before the marker, so a read-only drive fails on the message that
		// names what actually could not be done.
		if err := os.Mkdir(root, 0o755); err != nil && !os.IsExist(err) {
			return config.Target{}, fmt.Errorf("could not create the folder %q on that storage (is it read-only?): %w",
				filepath.Base(root), err)
		}
	}
	fi, err := os.Stat(root)
	if err != nil || !fi.IsDir() {
		return config.Target{}, fmt.Errorf("drive path not found or not a directory: %s", root)
	}
	if name == "" {
		name = filepath.Base(root)
	}
	if err := CheckNameFree(cfg, name); err != nil {
		return config.Target{}, err
	}
	if err := EnsureTargetMarkerAs(localmirror.NewLocalFS(root), name, cfg.General.MachineName, takeOver); err != nil {
		return config.Target{}, locate(err, "drive", root)
	}
	t := config.Target{Type: "drive", Name: name, Path: root, Folders: []string{}}
	cfg.Targets = append(cfg.Targets, t)
	return t, nil
}

// ValidateFolderName accepts one path component that is about to become a
// directory on a destination.
//
// IT REJECTS RATHER THAN SANITIZES, which is the opposite of what config's
// sanitize does for machine names and labels — and deliberately so. Rewriting a
// character is right for a name the user never typed and will not go looking
// for. It is wrong for one they are looking at in a box: a folder that comes
// back called "my_backups" when they asked for "my:backups" is a folder they
// will not recognise on the drive.
func ValidateFolderName(name string) error {
	switch {
	case name == "":
		return errors.New("give the folder a name")
	case name == "." || name == "..":
		return fmt.Errorf("%q is not a folder name", name)
	case strings.ContainsAny(name, `/\`):
		return errors.New("a folder name cannot contain a slash — this makes one folder, not a path")
	case strings.HasPrefix(name, "."):
		return errors.New("a folder name starting with a dot would be hidden on most systems; choose another")
	case strings.ContainsAny(name, `<>:"|?*`):
		return errors.New(`a folder name cannot contain any of < > : " | ? * — drives and network shares refuse them`)
	case strings.TrimSpace(name) != name:
		return errors.New("a folder name cannot start or end with a space")
	}
	return nil
}
