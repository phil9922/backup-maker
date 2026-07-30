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
	"net"
	"path/filepath"
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
	// ExistingFolder is set when that target is a FOLDER on this drive rather
	// than the drive itself. Without it a drive already in use through a
	// subfolder renders as unconfigured, and the obvious next click sets the
	// same drive up a second time at its root.
	ExistingFolder string `json:"existing_folder,omitempty"`
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

	// Network destinations THIS COMPUTER IS ALREADY USING, listed without a
	// scan.
	//
	// They were missing entirely: only paired machines were read out of the
	// config, so a share already holding this machine's backups appeared
	// nowhere until you went looking for it on the network — and then it
	// arrived as a stranger, asking for a password the daemon had stored all
	// along. Adding a second folder to a destination already in daily use
	// meant a network scan and retyping credentials, every single time.
	//
	// Deduplicated against anything the scan already produced, so a share that
	// is both configured and discovered is one machine, not two.
	seen := map[string]bool{}
	for _, m := range out {
		if m.Addr != "" {
			seen[m.Addr] = true
		}
	}
	for _, t := range cfg.Targets {
		if t.Type != "share" {
			continue
		}
		addr, err := ShareAddr(t.URL)
		if err != nil || seen[addr] {
			continue
		}
		seen[addr] = true
		out = append(out, Machine{
			ID:        addr,
			Name:      t.Name,
			Kind:      KindSMB,
			Addr:      addr,
			Browsable: true,
			// The credentials are already stored, so the caller supplies them
			// rather than the user: see StorageFor's contract.
			NeedsAuth: false,
			Note:      "already set up here — no password needed",
		})
	}
	return out, scanErr
}

// ShareAddr is the host part of a share URL, in the form StorageFor expects as
// a machine id (host, or host:port when it is not the default). Exported so the
// daemon can match a stored credential to the machine being browsed.
func ShareAddr(rawURL string) (string, error) {
	host, port, _, _, err := smbfs.Parse(rawURL)
	if err != nil {
		return "", err
	}
	if port != "" && port != "445" {
		return net.JoinHostPort(host, port), nil
	}
	return host, nil
}

// StorageFor lists what can be selected on one machine. user/pass are only
// consulted for a locked SMB host.
//
// drives, listShares and usage may each be nil, which selects the real
// implementation; they exist so both branches can be exercised without
// hardware or a network, the way List takes its scan.
func StorageFor(ctx context.Context, cfg *config.Config, machineID, user, pass string,
	drives func() []browse.Drive,
	listShares func(ctx context.Context, addr, user, pass string) ([]string, error),
	usage func(url, user, pass string) (free, total uint64)) ([]Storage, error) {
	if drives == nil {
		drives = browse.Drives
	}
	if listShares == nil {
		listShares = smbfs.ListShares
	}
	if usage == nil {
		usage = shareUsage
	}

	if machineID == KindThis {
		return localStorage(cfg, drives), nil
	}
	for _, t := range cfg.Targets {
		if t.Type == "device" && t.DeviceID == machineID {
			return nil, fmt.Errorf("%q decides where backups land itself — set its folder over there with: backup-maker receive enable --root <path>", t.Name)
		}
	}
	shares, err := listShares(ctx, machineID, user, pass)
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
		st.Free, st.Total = usage(url, user, pass)
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

func localStorage(cfg *config.Config, drives func() []browse.Drive) []Storage {
	found := drives()
	out := make([]Storage, 0, len(found))
	for _, d := range found {
		s := Storage{
			Kind:  "drive",
			Label: d.Label,
			Path:  d.Path,
			Free:  d.Free,
			Total: d.Total,
		}
		s.ExistingTarget, s.ExistingFolder = targetOn(cfg, d.Path)
		out = append(out, s)
	}
	return out
}

// targetOn finds the configured drive target using this drive — either the
// drive itself, or a folder on it — and returns the target name plus that
// folder's name when it is one.
//
// A PREFIX MATCH, WITH THE SEPARATOR CHECKED. A bare strings.HasPrefix would
// match "/media/alex/CARD" against a drive at "/media/alex/CARD-OLD" and report a
// completely different drive as already set up. The exact match is preferred
// over any subfolder, so a drive used both ways still names its root target.
func targetOn(cfg *config.Config, drive string) (target, folder string) {
	for _, t := range cfg.Targets {
		if t.Type == "drive" && config.SameDest(t.Path, drive) {
			return t.Name, ""
		}
	}
	prefix := strings.TrimSuffix(drive, string(filepath.Separator)) + string(filepath.Separator)
	for _, t := range cfg.Targets {
		if t.Type != "drive" || len(t.Path) <= len(prefix) {
			continue
		}
		if config.SameDest(t.Path[:len(prefix)], prefix) {
			return t.Name, filepath.Base(t.Path)
		}
	}
	return "", ""
}
