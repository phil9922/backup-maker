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
	return AddShareTargetAs(url, username, password, name, verify, false)
}

// AddShareTargetAs is AddShareTarget plus the name-takeover decision.
func AddShareTargetAs(url, username, password, name string, verify, takeOver bool) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	if _, _, _, _, perr := smbfs.Parse(url); perr != nil {
		return perr
	}
	name, nerr := TargetName(cfg, name, DefaultShareTargetName(url))
	if nerr != nil {
		return nerr
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
	if err := EnsureTargetMarkerAs(backend, name, cfg.General.MachineName, takeOver); err != nil {
		return locate(err, "share", url)
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
	return EnsureTargetMarkerAs(b, targetName, machineName, false)
}

// EnsureTargetMarkerAs is EnsureTargetMarker plus the claim on this machine's
// own directory at the destination.
//
// THE ONE CHOKE POINT EVERY ADD PATH GOES THROUGH — the CLI's add-target drive
// and share, the wizard's drive and share, and resolveDestination — which is
// why the check belongs here rather than in each of them.
//
// takeOver is a decision a person made in front of the conflict message. It is
// never inferred: a directory another installation holds is refused, because
// taking it over means two computers writing into one tree, and each pass of
// each mirror would then version the other's files away.
//
// The rules differ from the daemon's on purpose. Here, a directory that is
// unclaimed BUT already holds backups is refused too, and the caller is asked:
// a human is present, and from here "my own backups from before the upgrade"
// and "the other laptop's" look identical. The daemon, with nobody to ask,
// takes an unclaimed directory instead — otherwise replacing the binary would
// stop every working backup on the machine.
func EnsureTargetMarkerAs(b localmirror.Backend, targetName, machineName string, takeOver bool) error {
	state, err := config.LoadState()
	if err != nil {
		return err
	}
	if state.InstallID == "" {
		state.InstallID = config.NewToken()[:16]
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

	machineDir := config.MachineDir(machineName)
	st, holder := localmirror.CheckClaim(b, machineDir, state.Owns)
	switch {
	case st == localmirror.ClaimOurs:
		// Already ours; nothing to decide.
	case st == localmirror.ClaimOther, st == localmirror.ClaimUnreadable:
		if !takeOver {
			return claimConflict(holder, machineName, targetName, false)
		}
		if err := localmirror.WriteClaim(b, machineDir, state.InstallID, machineName); err != nil {
			return fmt.Errorf("cannot claim this computer's folder on that storage: %w", err)
		}
	default: // ClaimUnclaimed
		if !takeOver && localmirror.MachineDirIsInUse(b, machineDir) {
			return claimConflict(nil, machineName, targetName, true)
		}
		if err := localmirror.WriteClaim(b, machineDir, state.InstallID, machineName); err != nil {
			return fmt.Errorf("cannot claim this computer's folder on that storage: %w", err)
		}
	}

	if state.DriveTargetUUIDs == nil {
		state.DriveTargetUUIDs = map[string]string{}
	}
	state.DriveTargetUUIDs[targetName] = uuid
	return state.Save()
}

// claimConflict builds the error the caller shows, carrying enough for the
// wizard to offer buttons rather than a paragraph.
func claimConflict(holder *localmirror.Claim, machineName, targetName string, legacy bool) error {
	e := &ClaimConflictError{
		MachineName: machineName,
		TargetName:  targetName,
		Legacy:      legacy,
	}
	if holder != nil {
		e.Claimed = holder.Claimed
	}
	return e
}
