// SPDX-License-Identifier: MIT

// Package machines presents backup destinations the way people actually think
// about them: a list of computers, each of which has storage you can pick.
//
// It deliberately hides the distinction between "a drive plugged into this
// computer", "an SMB share address", and "a paired machine" — those are
// implementation details of how the bytes travel, not a question a user should
// have to answer before they can choose where their backup goes.
//
// This package adds no scanning of its own; it composes discover, browse, and
// smbfs. Network scanning still happens only when explicitly requested.
package machines

import (
	"context"
	"fmt"
	"strings"

	"github.com/phil9922/backup-maker/internal/browse"
	"github.com/phil9922/backup-maker/internal/config"
	"github.com/phil9922/backup-maker/internal/discover"
	"github.com/phil9922/backup-maker/internal/smbfs"
)

// Machine kinds.
const (
	KindThis   = "this"   // this computer; storage is locally attached
	KindSMB    = "smb"    // a file-sharing computer or NAS found on the network
	KindPaired = "paired" // another machine running backup-maker
)

// Machine is one computer that could hold backups.
type Machine struct {
	// ID is the stable handle the UI passes back to fetch storage:
	// "this", an IP address, or a device ID.
	ID   string `json:"id"`
	Name string `json:"name"`
	Kind string `json:"kind"`
	Addr string `json:"addr,omitempty"`
	// NeedsAuth means storage can't be listed until credentials are supplied.
	NeedsAuth bool `json:"needs_auth,omitempty"`
	// Browsable is false for paired machines, which choose their own
	// destination folder; Note explains why.
	Browsable bool   `json:"browsable"`
	Note      string `json:"note,omitempty"`
}

// Storage is one selectable place on a machine.
//
// The word is deliberately vague: over SMB this is a *share*, which may be a
// whole disk, a USB stick, or merely a folder someone chose to share. Claiming
// to see "drives attached to that computer" would be a lie.
type Storage struct {
	Kind  string `json:"kind"` // "drive" or "share"
	Label string `json:"label"`
	Path  string `json:"path,omitempty"` // drive: local mount point
	URL   string `json:"url,omitempty"`  // share: //host/share
	Free  uint64 `json:"free,omitempty"`
	Total uint64 `json:"total,omitempty"`
	// ExistingTarget names the already-configured target using this storage,
	// so the UI can offer it instead of creating a duplicate.
	ExistingTarget string `json:"existing_target,omitempty"`
}

// List returns the computers worth showing, this one first. scan may be nil to
// skip network discovery (the caller decides when a scan is appropriate —
// scanning is never implicit).
func List(ctx context.Context, cfg *config.Config, scan func(context.Context) ([]discover.Host, error)) ([]Machine, error) {
	out := []Machine{{
		ID:        KindThis,
		Name:      cfg.General.MachineName,
		Kind:      KindThis,
		Browsable: true,
		Note:      "drives plugged into or inside this computer",
	}}

	var scanErr error
	if scan != nil {
		hosts, err := scan(ctx)
		if err != nil {
			scanErr = err
		}
		for _, h := range hosts {
			name := h.Name
			if name == "" || name == h.Addr {
				name = h.Addr
			}
			out = append(out, Machine{
				ID:        h.Addr,
				Name:      name,
				Kind:      KindSMB,
				Addr:      h.Addr,
				NeedsAuth: h.NeedsAuth,
				Browsable: true,
			})
		}
	}

	// Paired machines are already configured destinations; they can't be
	// browsed because the receiving side picks its own root.
	for _, t := range cfg.Targets {
		if t.Type != "device" {
			continue
		}
		out = append(out, Machine{
			ID:        t.DeviceID,
			Name:      t.Name,
			Kind:      KindPaired,
			Browsable: false,
			Note:      "this machine chooses where backups land, using its own \"receive\" setting",
		})
	}
	return out, scanErr
}

// StorageFor lists what can be selected on one machine. user/pass are only
// consulted for a locked SMB host.
func StorageFor(ctx context.Context, cfg *config.Config, machineID, user, pass string) ([]Storage, error) {
	if machineID == KindThis {
		return localStorage(cfg), nil
	}
	for _, t := range cfg.Targets {
		if t.Type == "device" && t.DeviceID == machineID {
			return nil, fmt.Errorf("%q decides where backups land itself — set its folder over there with: backup-maker receive enable --root <path>", t.Name)
		}
	}
	shares, err := smbfs.ListShares(ctx, machineID, user, pass)
	if err != nil {
		if user == "" {
			return nil, fmt.Errorf("%w (this computer needs a username and password)", err)
		}
		return nil, err
	}

	byURL := map[string]string{}
	for _, t := range cfg.Targets {
		if t.Type == "share" {
			byURL[strings.ToLower(t.URL)] = t.Name
		}
	}
	out := make([]Storage, 0, len(shares))
	for _, s := range shares {
		url := "//" + machineID + "/" + s
		st := Storage{
			Kind:           "share",
			Label:          s,
			URL:            url,
			ExistingTarget: byURL[strings.ToLower(url)],
		}
		st.Free, st.Total = shareUsage(url, user, pass)
		out = append(out, st)
	}
	return out, nil
}

// shareUsage reports a share's free/total by opening it briefly. Best-effort:
// a share the caller can list but not open (or one that errors on statfs)
// simply shows no capacity line rather than failing the whole picker. This
// costs one SMB connection per share while the picker loads, which is the
// price of showing capacity before a destination is chosen.
func shareUsage(url, user, pass string) (free, total uint64) {
	fs, err := smbfs.New(url, user, pass)
	if err != nil {
		return 0, 0
	}
	defer fs.Close()
	free, total, err = fs.Usage()
	if err != nil {
		return 0, 0
	}
	return free, total
}

func localStorage(cfg *config.Config) []Storage {
	byPath := map[string]string{}
	for _, t := range cfg.Targets {
		if t.Type == "drive" {
			byPath[t.Path] = t.Name
		}
	}
	drives := browse.Drives()
	out := make([]Storage, 0, len(drives))
	for _, d := range drives {
		out = append(out, Storage{
			Kind:           "drive",
			Label:          d.Label,
			Path:           d.Path,
			Free:           d.Free,
			Total:          d.Total,
			ExistingTarget: byPath[d.Path],
		})
	}
	return out
}
