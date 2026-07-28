// SPDX-License-Identifier: MIT

package config

import (
	"os"
	"path/filepath"
)

// DirName is the leaf name of the configuration/data directory. It is NOT a
// pattern anything may match on: see SelfExcludes.
const DirName = "backup-maker"

// dirPath is where the configuration/data directory lives, without creating
// it. Separate from Dir because the exclusion rules have to ask where it would
// be without bringing it into existence as a side effect.
func dirPath() (string, error) {
	base, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(base, DirName), nil
}

// Dir returns the backup-maker configuration/data directory, creating it if
// needed. Linux: ~/.config/backup-maker, macOS: ~/Library/Application
// Support/backup-maker, Windows: %AppData%\backup-maker.
func Dir() (string, error) {
	dir, err := dirPath()
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	return dir, nil
}

func ConfigPath() (string, error) {
	dir, err := Dir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "config.toml"), nil
}

func StatePath() (string, error) {
	dir, err := Dir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "state.json"), nil
}

func LockPath() (string, error) {
	dir, err := Dir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "daemon.lock"), nil
}

func LogDir() (string, error) {
	dir, err := Dir()
	if err != nil {
		return "", err
	}
	logs := filepath.Join(dir, "logs")
	if err := os.MkdirAll(logs, 0o700); err != nil {
		return "", err
	}
	return logs, nil
}
