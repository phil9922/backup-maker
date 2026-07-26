// SPDX-License-Identifier: MIT

// Package pairing handles the receive side of machine-to-machine backups:
// surfacing pending devices for user approval and auto-accepting folder
// offers from already-approved sources with receive-only + versioning
// enforced.
package pairing

import (
	"encoding/json"
	"errors"
	"log/slog"
	"path/filepath"
	"strings"

	"github.com/phil9922/backup-maker/internal/config"
	"github.com/phil9922/backup-maker/internal/syncthing"
)

// ProcessPendingFolders accepts every folder offered by an approved backup
// source, creating it as receiveonly with staggered versioning under
// <receive.root>/<source-name>/<label>. Offers from unknown devices are left
// pending (they surface in status until the user approves the device).
func ProcessPendingFolders(c *syncthing.Client, cfg *config.Config, log *slog.Logger) error {
	if !cfg.Receive.Enabled {
		return nil
	}
	pending, err := c.PendingFolders()
	if err != nil {
		return err
	}
	if len(pending) == 0 {
		return nil
	}

	approved := map[string]bool{}
	for _, id := range cfg.Receive.AcceptedSources {
		approved[id] = true
	}
	deviceNames := deviceNameMap(c)

	for folderID, offer := range pending {
		for deviceID, info := range offer.OfferedBy {
			if !approved[deviceID] {
				continue
			}
			label := info.Label
			if label == "" {
				label = folderID
			}
			sourceName := deviceNames[deviceID]
			if sourceName == "" {
				sourceName = shortID(deviceID)
			}
			path := filepath.Join(cfg.Receive.Root, sanitize(sourceName), sanitize(label))
			folder := map[string]any{
				"id":               folderID,
				"label":            label,
				"path":             path,
				"type":             "receiveonly",
				"devices":          []syncthing.FolderDevice{{DeviceID: deviceID}},
				"fsWatcherEnabled": true,
				"fsWatcherDelayS":  2,
				"rescanIntervalS":  3600,
				"versioning":       syncthing.StaggeredVersioning(cfg.Defaults.VersioningMaxAgeDays),
			}
			if err := c.PutFolderOne(folderID, folder); err != nil {
				return err
			}
			log.Info("accepted backup folder", "id", folderID, "from", sourceName, "path", path)
		}
	}
	return nil
}

// PendingSources lists devices asking to pair that the user has not approved.
type PendingSource struct {
	DeviceID string `json:"device_id"`
	Name     string `json:"name"`
	Address  string `json:"address"`
}

func PendingSources(c *syncthing.Client, cfg *config.Config) ([]PendingSource, error) {
	pending, err := c.PendingDevices()
	if err != nil {
		return nil, err
	}
	approved := map[string]bool{}
	for _, id := range cfg.Receive.AcceptedSources {
		approved[id] = true
	}
	for _, t := range cfg.Targets {
		if t.Type == "device" {
			approved[t.DeviceID] = true
		}
	}
	var out []PendingSource
	for id, info := range pending {
		if !approved[id] {
			out = append(out, PendingSource{DeviceID: id, Name: info.Name, Address: info.Address})
		}
	}
	return out, nil
}

// AcceptSource approves a machine as a backup source and saves it. The bool
// reports whether it was newly approved, so a caller can tell the user
// "approved" apart from "already approved".
//
// The ONE place the approved list is written: `pair accept` and the dashboard's
// Approve button both come through here, because an approval that means
// something slightly different depending on where it was clicked is exactly the
// kind of drift that ends with backups quietly not being accepted.
//
// Config is re-read here rather than taken from the caller, so an approval
// never clobbers a change written while the daemon was running.
func AcceptSource(deviceID string) (bool, error) {
	cfg, err := config.Load()
	if err != nil {
		return false, err
	}
	if !cfg.Receive.Enabled {
		return false, errors.New("enable receiving first: backup-maker receive enable --root <path>")
	}
	id := strings.ToUpper(strings.TrimSpace(deviceID))
	if id == "" {
		return false, errors.New("no device ID given")
	}
	for _, s := range cfg.Receive.AcceptedSources {
		if s == id {
			return false, nil
		}
	}
	cfg.Receive.AcceptedSources = append(cfg.Receive.AcceptedSources, id)
	if err := cfg.Save(); err != nil {
		return false, err
	}
	return true, nil
}

func deviceNameMap(c *syncthing.Client) map[string]string {
	out := map[string]string{}
	raw, err := c.RawDevices()
	if err != nil {
		return out
	}
	for _, r := range raw {
		var d syncthing.Device
		if json.Unmarshal(r, &d) == nil {
			out[d.DeviceID] = d.Name
		}
	}
	return out
}

func sanitize(name string) string {
	out := []rune(name)
	for i, r := range out {
		switch r {
		case '<', '>', ':', '"', '/', '\\', '|', '?', '*':
			out[i] = '_'
		}
	}
	if len(out) == 0 {
		return "unnamed"
	}
	return string(out)
}

func shortID(deviceID string) string {
	if i := strings.IndexByte(deviceID, '-'); i > 0 {
		return deviceID[:i]
	}
	return deviceID
}
