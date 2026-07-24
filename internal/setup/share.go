// SPDX-License-Identifier: MIT

// Package setup holds target-creation flows shared by the CLI and the
// dashboard API.
package setup

import (
	"fmt"

	"github.com/phil9922/backup-maker/internal/config"
	"github.com/phil9922/backup-maker/internal/localmirror"
	"github.com/phil9922/backup-maker/internal/smbfs"
)

// AddShareTarget validates, tests, stamps, and saves a network-drive target.
// The password lands in state.json; the target in config.toml (which a
// running daemon picks up automatically).
func AddShareTarget(url, username, password, name string, verify bool) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	if _, _, share, _, perr := smbfs.Parse(url); perr != nil {
		return perr
	} else if name == "" {
		name = share
	}
	for _, t := range cfg.Targets {
		if t.Name == name {
			return fmt.Errorf("a target named %q already exists", name)
		}
	}

	if err := smbfs.TestConnection(url, username, password); err != nil {
		if username == "" {
			return fmt.Errorf("%w (this share may require credentials)", err)
		}
		return err
	}

	backend, err := smbfs.New(url, username, password)
	if err != nil {
		return err
	}
	defer backend.Close()
	if err := EnsureTargetMarker(backend, name, cfg.General.MachineName); err != nil {
		return err
	}

	state, err := config.LoadState()
	if err != nil {
		return err
	}
	if state.ShareCredentials == nil {
		state.ShareCredentials = map[string]string{}
	}
	state.ShareCredentials[name] = password
	if err := state.Save(); err != nil {
		return err
	}

	t := config.Target{Type: "share", Name: name, URL: url, Username: username, Folders: []string{}}
	if !verify {
		f := false
		t.Verify = &f
	}
	cfg.Targets = append(cfg.Targets, t)
	return cfg.Save()
}

// EnsureTargetMarker stamps (or recognizes) a backend root as a backup target
// and records its UUID in state under the target name.
func EnsureTargetMarker(b localmirror.Backend, targetName, machineName string) error {
	state, err := config.LoadState()
	if err != nil {
		return err
	}
	uuid := ""
	if m, err := localmirror.ReadMarker(b); err == nil {
		uuid = m.TargetUUID
	} else {
		uuid = config.NewToken()[:16]
		if err := localmirror.WriteMarker(b, uuid, machineName); err != nil {
			return fmt.Errorf("cannot write to target (is it read-only?): %w", err)
		}
	}
	if state.DriveTargetUUIDs == nil {
		state.DriveTargetUUIDs = map[string]string{}
	}
	state.DriveTargetUUIDs[targetName] = uuid
	return state.Save()
}
