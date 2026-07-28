// SPDX-License-Identifier: MIT

package setup

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/phil9922/backup-maker/internal/config"
	"github.com/phil9922/backup-maker/internal/localmirror"
)

// ManifestName is the non-secret inventory written at each target root, beside
// the identity marker (MarkerName). It is what lets a fresh install ADOPT an
// existing destination: point backup-maker at a drive or share that already
// holds backups and recover the whole configuration — folders, labels,
// excludes, every destination, schedules and the machine name — without the
// original machine present.
//
// It deliberately stores NO secrets. Share and archive passwords never touch a
// destination; adoption re-collects them from the user, who already holds them.
// This is the same split that keeps passwords out of the shareable config.toml
// and in the private state.json.
const ManifestName = ".backup-maker-manifest.json"

// ManifestVersion is the on-disk format version, so a later field change can be
// migrated rather than silently mis-read by an older or newer binary.
const ManifestVersion = 1

// Manifest is the destination-side snapshot of a machine's backup
// configuration. Every field is drawn from the shareable config.toml (plus each
// target's anti-collision UUID, which already sits in plaintext in that
// target's marker), so by construction it carries nothing private.
type Manifest struct {
	Version     int              `json:"version"`
	MachineName string           `json:"machine_name"`
	Generated   time.Time        `json:"generated"`
	Defaults    config.Defaults  `json:"defaults"`
	Folders     []config.Folder  `json:"folders"`
	Targets     []ManifestTarget `json:"targets"`
	Archives    []config.Archive `json:"archives"`
}

// ManifestTarget is a configured destination plus the UUID that identifies its
// on-disk marker. Recording the UUID lets adoption match a plugged-in drive to
// the exact target entry, and lets every OTHER destination resume the moment it
// is reconnected — the daemon recognizes a target only when the recorded UUID
// matches the marker it finds. Device targets carry no UUID (a paired machine
// is identified by its DeviceID); the field is then omitted.
type ManifestTarget struct {
	config.Target
	UUID string `json:"uuid,omitempty"`
}

// BuildManifest snapshots a config into a manifest stamped at the given time.
// uuids maps target name to the UUID recorded for it in the private state
// (drive and share targets only); missing entries leave UUID empty.
func BuildManifest(cfg *config.Config, uuids map[string]string, now time.Time) Manifest {
	targets := make([]ManifestTarget, len(cfg.Targets))
	for i, t := range cfg.Targets {
		targets[i] = ManifestTarget{Target: t, UUID: uuids[t.Name]}
	}
	return Manifest{
		Version:     ManifestVersion,
		MachineName: cfg.General.MachineName,
		Generated:   now,
		Defaults:    cfg.Defaults,
		Folders:     cfg.Folders,
		Targets:     targets,
		Archives:    cfg.Archives,
	}
}

// WriteManifest refreshes the manifest at a target's backend root. Callers
// treat failure as non-fatal: an offline drive or an unreachable share just
// keeps an older manifest until it is next reachable. uuids is the target-name
// to marker-UUID map from the private state (config.State.DriveTargetUUIDs).
func WriteManifest(b localmirror.Backend, cfg *config.Config, uuids map[string]string) error {
	m := BuildManifest(cfg, uuids, time.Now())
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	return b.WriteFile(ManifestName, data)
}

// ReadManifest loads the manifest at a target's backend root, if present.
func ReadManifest(b localmirror.Backend) (*Manifest, error) {
	data, err := b.ReadFile(ManifestName)
	if err != nil {
		return nil, err
	}
	var m Manifest
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("corrupt backup manifest: %w", err)
	}
	return &m, nil
}

// ReadManifestAt is an os-path convenience for adopting a locally mounted
// destination (drive, SD card, USB stick).
func ReadManifestAt(root string) (*Manifest, error) {
	return ReadManifest(localmirror.NewLocalFS(root))
}
