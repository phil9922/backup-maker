// SPDX-License-Identifier: MIT

package syncthing

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"path/filepath"
	"strings"

	"github.com/phil9922/backup-maker/internal/config"
)

// Reconcile drives syncthing's config to match ours: network posture, device
// targets, send-only folders with ignores, and receive-only folders for
// accepted sources. Safe to call repeatedly.
func Reconcile(c *Client, cfg *config.Config, log *slog.Logger) error {
	if err := reconcileOptions(c, cfg); err != nil {
		return fmt.Errorf("options: %w", err)
	}
	if err := reconcileDevices(c, cfg, log); err != nil {
		return fmt.Errorf("devices: %w", err)
	}
	if err := reconcileSendFolders(c, cfg, log); err != nil {
		return fmt.Errorf("folders: %w", err)
	}
	if err := EnforceReceiveOnly(c, cfg, log); err != nil {
		return fmt.Errorf("receive folders: %w", err)
	}
	if ok, err := c.ConfigInSync(); err == nil && !ok {
		log.Info("engine restart required to apply config")
		return c.Restart()
	}
	return nil
}

// reconcileOptions locks the sync engine to the local network. These are not
// defaults to be overridden — there is deliberately no setting that turns any
// of them back on:
//
//   - globalAnnounceEnabled: never announce this device to public discovery
//     servers, so its ID is not published anywhere.
//   - relaysEnabled / natEnabled: never route through third-party relays or
//     punch holes outward.
//   - urAccepted -1 / crashReportingEnabled false: no telemetry of any kind.
//
// An earlier build exposed an "allow-relays" mode, but it could never have
// worked: without global discovery two machines in different buildings have no
// way to find each other, so it promised off-site sync it could not deliver.
// Rather than publish device IDs to make it work, backup-maker stays local.
func reconcileOptions(c *Client, cfg *config.Config) error {
	return c.PatchOptions(map[string]any{
		"globalAnnounceEnabled": false,
		"localAnnounceEnabled":  true,
		"relaysEnabled":         false,
		"natEnabled":            false,
		"urAccepted":            -1,
		"crashReportingEnabled": false,
		"startBrowser":          false,
	})
}

// deviceTargets returns config targets of type "device" keyed by device ID.
func deviceTargets(cfg *config.Config) map[string]config.Target {
	out := map[string]config.Target{}
	for _, t := range cfg.Targets {
		if t.Type == "device" {
			out[t.DeviceID] = t
		}
	}
	return out
}

func reconcileDevices(c *Client, cfg *config.Config, log *slog.Logger) error {
	raw, err := c.RawDevices()
	if err != nil {
		return err
	}
	myID, err := c.MyID()
	if err != nil {
		return err
	}
	existing := map[string]bool{}
	for _, r := range raw {
		var d Device
		if err := json.Unmarshal(r, &d); err != nil {
			continue
		}
		existing[d.DeviceID] = true
		// Advertise our backup-maker machine name to peers.
		if d.DeviceID == myID && d.Name != cfg.General.MachineName {
			merged, _, merr := mergeOver(r, map[string]any{"name": cfg.General.MachineName})
			if merr == nil {
				if err := c.PutDeviceOne(myID, merged); err != nil {
					return err
				}
			}
		}
	}

	wanted := deviceTargets(cfg)
	for _, src := range cfg.Receive.AcceptedSources {
		if _, ok := wanted[src]; !ok {
			// Empty name: syncthing fills in the peer's advertised name on
			// first connect, so received backups get a human-readable path.
			wanted[src] = config.Target{Type: "device", DeviceID: src, Name: ""}
		}
	}

	for id, t := range wanted {
		if existing[id] {
			continue
		}
		dev := Device{
			DeviceID:          id,
			Name:              t.Name,
			Addresses:         []string{"dynamic"},
			AutoAcceptFolders: false, // we accept folders ourselves so we control receiveonly+versioning
		}
		if err := c.PutDeviceOne(id, dev); err != nil {
			return err
		}
		log.Info("device added to engine", "device", shortID(id), "name", t.Name)
	}
	return nil
}

func reconcileSendFolders(c *Client, cfg *config.Config, log *slog.Logger) error {
	raw, err := c.RawFolders()
	if err != nil {
		return err
	}
	existingRaw := map[string]json.RawMessage{}
	for _, r := range raw {
		var f Folder
		if err := json.Unmarshal(r, &f); err != nil {
			continue
		}
		existingRaw[f.ID] = r
	}

	// Which device targets back up each folder?
	folderDevices := map[string][]FolderDevice{}
	for _, t := range cfg.Targets {
		if t.Type != "device" {
			continue
		}
		for _, f := range cfg.FoldersForTarget(t) {
			folderDevices[f.ID] = append(folderDevices[f.ID], FolderDevice{DeviceID: t.DeviceID})
		}
	}

	ours := map[string]bool{}
	for _, f := range cfg.Folders {
		ours[f.ID] = true
		desired := map[string]any{
			"id":               f.ID,
			"label":            f.Label,
			"path":             f.Path,
			"type":             "sendonly",
			"devices":          folderDevices[f.ID],
			"fsWatcherEnabled": true,
			"fsWatcherDelayS":  2,
			"rescanIntervalS":  3600,
			"versioning":       Versioning{Type: "", Params: map[string]string{}}, // source keeps no versions
		}
		merged, changed, err := mergeOver(existingRaw[f.ID], desired)
		if err != nil {
			return err
		}
		if changed {
			if err := c.PutFolderOne(f.ID, merged); err != nil {
				return err
			}
			log.Info("send folder configured", "id", f.ID, "path", f.Path, "devices", len(folderDevices[f.ID]))
		}
		if err := c.SetIgnores(f.ID, IgnoreLines(cfg, f)); err != nil {
			return err
		}
	}

	// Folders we created earlier but that left the config: remove from engine.
	for _, r := range raw {
		var f Folder
		if err := json.Unmarshal(r, &f); err != nil {
			continue
		}
		if f.Type == "sendonly" && !ours[f.ID] {
			if err := c.DeleteFolder(f.ID); err != nil {
				return err
			}
			log.Info("send folder removed", "id", f.ID)
		}
	}
	return nil
}

// EnforceReceiveOnly re-asserts type=receiveonly and versioning on every
// backup folder this machine receives; called on reconcile and whenever the
// engine reports a config change.
func EnforceReceiveOnly(c *Client, cfg *config.Config, log *slog.Logger) error {
	if !cfg.Receive.Enabled {
		return nil
	}
	raw, err := c.RawFolders()
	if err != nil {
		return err
	}
	for _, r := range raw {
		var f Folder
		if err := json.Unmarshal(r, &f); err != nil {
			continue
		}
		if !strings.HasPrefix(filepath.Clean(f.Path), filepath.Clean(cfg.Receive.Root)) {
			continue // not one of our received backups
		}
		if f.Type == "receiveonly" && f.Versioning.Type == "staggered" {
			continue
		}
		desired := map[string]any{
			"type":       "receiveonly",
			"versioning": StaggeredVersioning(cfg.Defaults.VersioningMaxAgeDays),
		}
		merged, _, err := mergeOver(r, desired)
		if err != nil {
			return err
		}
		if err := c.PutFolderOne(f.ID, merged); err != nil {
			return err
		}
		log.Warn("re-enforced receive-only on backup folder", "id", f.ID)
	}
	return nil
}

// StaggeredVersioning builds syncthing's staggered versioner config keeping
// maxAgeDays of history.
func StaggeredVersioning(maxAgeDays int) Versioning {
	if maxAgeDays <= 0 {
		maxAgeDays = config.DefaultVersioningMaxDays
	}
	return Versioning{
		Type: "staggered",
		Params: map[string]string{
			"cleanInterval": "3600",
			"maxAge":        fmt.Sprint(maxAgeDays * 24 * 3600),
		},
	}
}

// IgnoreLines converts a folder's ignore config into syncthing ignore
// patterns. The (?d) prefix lets syncthing delete ignored junk when the
// parent goes away.
func IgnoreLines(cfg *config.Config, f config.Folder) []string {
	var pats []string
	if !f.NoDefaultIgnores {
		pats = append(pats, cfg.Defaults.Ignore...)
	}
	pats = append(pats, f.ExtraIgnore...)
	lines := make([]string, 0, len(pats))
	for _, p := range pats {
		lines = append(lines, "(?d)"+p)
	}
	return lines
}

// mergeOver overlays desired fields onto an existing raw JSON object (nil for
// new), reporting whether anything changed.
func mergeOver(existing json.RawMessage, desired map[string]any) (map[string]any, bool, error) {
	out := map[string]any{}
	if existing != nil {
		if err := json.Unmarshal(existing, &out); err != nil {
			return nil, false, err
		}
	}
	changed := existing == nil
	for k, v := range desired {
		nv, err := json.Marshal(v)
		if err != nil {
			return nil, false, err
		}
		ov, _ := json.Marshal(out[k])
		if string(nv) != string(ov) {
			changed = true
		}
		var plain any
		if err := json.Unmarshal(nv, &plain); err != nil {
			return nil, false, err
		}
		out[k] = plain
	}
	return out, changed, nil
}

func shortID(deviceID string) string {
	if i := strings.IndexByte(deviceID, '-'); i > 0 {
		return deviceID[:i]
	}
	if len(deviceID) > 7 {
		return deviceID[:7]
	}
	return deviceID
}
