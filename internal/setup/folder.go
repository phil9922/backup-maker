// SPDX-License-Identifier: MIT

package setup

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/phil9922/backup-maker/internal/config"
)

// ValidateFolder resolves and checks a candidate backup source without saving
// anything. Split out so the dashboard can validate a picked path before the
// user commits, and so AddFolder and the multi-destination backup flow share
// one definition of "is this a usable folder".
func ValidateFolder(cfg *config.Config, path string) (string, error) {
	abs, err := filepath.Abs(ExpandHome(path))
	if err != nil {
		return "", err
	}
	fi, err := os.Stat(abs)
	if err != nil {
		return "", fmt.Errorf("folder not found: %s", abs)
	}
	if !fi.IsDir() {
		return "", fmt.Errorf("not a directory: %s", abs)
	}
	for _, f := range cfg.Folders {
		if f.Path == abs {
			return "", fmt.Errorf("already backed up as folder %q", f.ID)
		}
	}
	return abs, nil
}

// AddFolder starts backing up a folder. It saves config.toml; a running daemon
// picks the change up within seconds.
func AddFolder(path, label string, extraIgnore []string, noDefaults bool) (config.Folder, error) {
	cfg, err := config.Load()
	if err != nil {
		return config.Folder{}, err
	}
	f, err := AppendFolder(cfg, path, label, extraIgnore, noDefaults)
	if err != nil {
		return config.Folder{}, err
	}
	if err := cfg.Save(); err != nil {
		return config.Folder{}, err
	}
	return f, nil
}

// AppendFolder adds the folder to cfg in memory without saving. The
// multi-destination flow uses this so a whole backup can be committed (or
// abandoned) as one unit.
func AppendFolder(cfg *config.Config, path, label string, extraIgnore []string, noDefaults bool) (config.Folder, error) {
	abs, err := ValidateFolder(cfg, path)
	if err != nil {
		return config.Folder{}, err
	}
	if label == "" {
		label = filepath.Base(abs)
	}
	f := config.Folder{
		ID:               config.NewFolderID(),
		Path:             abs,
		Label:            label,
		ExtraIgnore:      extraIgnore,
		NoDefaultIgnores: noDefaults,
	}
	cfg.Folders = append(cfg.Folders, f)
	return f, nil
}

// ExpandHome resolves a leading ~ so typed paths behave the way users expect
// in both the CLI and the dashboard's manual-entry box.
func ExpandHome(p string) string {
	if p == "~" || len(p) > 1 && p[0] == '~' && (p[1] == '/' || p[1] == filepath.Separator) {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, p[1:])
		}
	}
	return p
}
