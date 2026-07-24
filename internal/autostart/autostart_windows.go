// SPDX-License-Identifier: MIT

//go:build windows

package autostart

import (
	"fmt"

	"golang.org/x/sys/windows/registry"
)

const runValue = "backup-maker"

// Enable adds an HKCU Run entry — the simplest no-admin autostart. The
// --background flag makes the daemon detach without a console window.
func Enable() error {
	exe, err := exePath()
	if err != nil {
		return err
	}
	key, err := registry.OpenKey(registry.CURRENT_USER,
		`Software\Microsoft\Windows\CurrentVersion\Run`, registry.SET_VALUE)
	if err != nil {
		return err
	}
	defer key.Close()
	return key.SetStringValue(runValue, fmt.Sprintf(`"%s" daemon --background`, exe))
}

func Disable() error {
	key, err := registry.OpenKey(registry.CURRENT_USER,
		`Software\Microsoft\Windows\CurrentVersion\Run`, registry.SET_VALUE)
	if err != nil {
		return err
	}
	defer key.Close()
	err = key.DeleteValue(runValue)
	if err == registry.ErrNotExist {
		return nil
	}
	return err
}

func Status() (string, error) {
	key, err := registry.OpenKey(registry.CURRENT_USER,
		`Software\Microsoft\Windows\CurrentVersion\Run`, registry.QUERY_VALUE)
	if err != nil {
		return "", err
	}
	defer key.Close()
	if _, _, err := key.GetStringValue(runValue); err != nil {
		return "not installed", nil
	}
	return "installed (login autostart)", nil
}
