// SPDX-License-Identifier: MIT

package setup

import (
	"fmt"
	"os"
	"path/filepath"

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
	cfg, err := config.Load()
	if err != nil {
		return config.Target{}, err
	}
	t, err := AppendDriveTarget(cfg, path, name)
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
	root, err := filepath.Abs(ExpandHome(path))
	if err != nil {
		return config.Target{}, err
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
	if err := EnsureTargetMarker(localmirror.NewLocalFS(root), name, cfg.General.MachineName); err != nil {
		return config.Target{}, err
	}
	t := config.Target{Type: "drive", Name: name, Path: root, Folders: []string{}}
	cfg.Targets = append(cfg.Targets, t)
	return t, nil
}
