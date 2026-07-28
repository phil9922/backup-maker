// SPDX-License-Identifier: MIT

package setup

import (
	"encoding/json"
	"fmt"
	"path"
	"strings"
	"time"

	"github.com/phil9922/backup-maker/internal/archive"
	"github.com/phil9922/backup-maker/internal/config"
	"github.com/phil9922/backup-maker/internal/localmirror"
)

// ManifestName is the non-secret inventory backup-maker writes for each machine
// that uses a destination. It is what lets a fresh install ADOPT an existing
// destination: point backup-maker at a drive or share that already holds
// backups and recover the whole configuration — folders, labels, excludes,
// every destination, schedules and the machine name — without the original
// machine present.
//
// IT LIVES INSIDE THE MACHINE'S OWN DIRECTORY (see ManifestPathFor), not at the
// destination root. At the root there is exactly one of it, so two computers
// sharing a drive overwrote each other's on every cycle — and a drive holding
// two machines' backups could only ever be adopted as whichever of them wrote
// last. The mirror engine never walks this level (its reconcile is rooted at
// <machine>/<label>), so a file here is out of reach of everything that
// versions or removes.
//
// It deliberately stores NO secrets. Share and archive passwords never touch a
// destination; adoption re-collects them from the user, who already holds them.
// This is the same split that keeps passwords out of the shareable config.toml
// and in the private state.json.
const ManifestName = ".backup-maker-manifest.json"

// ManifestVersion is the on-disk format version, so a later field change can be
// migrated rather than silently mis-read by an older or newer binary.
//
// 1 — at the destination root, one per destination.
// 2 — under <machine>/, one per machine using the destination.
const ManifestVersion = 2

// ManifestPathFor is where one machine's manifest lives on a destination.
func ManifestPathFor(machineName string) string {
	return path.Join(config.MachineDir(machineName), ManifestName)
}

// Manifest is the destination-side snapshot of a machine's backup
// configuration. Every field is drawn from the shareable config.toml (plus each
// target's anti-collision UUID, which already sits in plaintext in that
// target's marker), so by construction it carries nothing private.
type Manifest struct {
	Version     int    `json:"version"`
	MachineName string `json:"machine_name"`
	// InstallID identifies the installation that wrote this, so a machine
	// rebuilt by adopting this destination can inherit the claims the old one
	// left on destinations that are not plugged in at the time. Absent on
	// version 1 manifests, which is harmless: a destination old enough to have
	// one carries no claims either.
	InstallID string           `json:"install_id,omitempty"`
	Generated time.Time        `json:"generated"`
	Defaults  config.Defaults  `json:"defaults"`
	Folders   []config.Folder  `json:"folders"`
	Targets   []ManifestTarget `json:"targets"`
	Archives  []config.Archive `json:"archives"`
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
// (drive and share targets only); missing entries leave UUID empty. installID
// identifies the installation writing it and may be empty.
func BuildManifest(cfg *config.Config, uuids map[string]string, installID string, now time.Time) Manifest {
	targets := make([]ManifestTarget, len(cfg.Targets))
	for i, t := range cfg.Targets {
		targets[i] = ManifestTarget{Target: t, UUID: uuids[t.Name]}
	}
	return Manifest{
		Version:     ManifestVersion,
		MachineName: cfg.General.MachineName,
		InstallID:   installID,
		Generated:   now,
		Defaults:    cfg.Defaults,
		Folders:     cfg.Folders,
		Targets:     targets,
		Archives:    cfg.Archives,
	}
}

// WriteManifest refreshes this machine's manifest on a destination. Callers
// treat failure as non-fatal: an offline drive or an unreachable share just
// keeps an older manifest until it is next reachable. uuids is the target-name
// to marker-UUID map from the private state (config.State.DriveTargetUUIDs).
func WriteManifest(b localmirror.Backend, cfg *config.Config, uuids map[string]string, installID string) error {
	m := BuildManifest(cfg, uuids, installID, time.Now())
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	dir := config.MachineDir(cfg.General.MachineName)
	if err := b.MkdirAll(dir); err != nil {
		return err
	}
	return b.WriteFile(path.Join(dir, ManifestName), data)
}

// FoundManifest is one machine's manifest on a destination, and where it was
// found.
type FoundManifest struct {
	MachineName string
	Manifest    *Manifest
	// Legacy marks the single root-level manifest an older backup-maker wrote.
	// Adoption works from it exactly as before; it is flagged only so a drive
	// that holds both can prefer the current one.
	Legacy bool
}

// ListManifests finds every machine's manifest on a destination: one inside
// each machine directory, plus the legacy root-level file when one is there.
//
// THE LEGACY FILE IS SHADOWED, NOT MIGRATED AND NEVER DELETED. After an upgrade
// the root file is a fossil describing the same machine as the per-machine one
// beside it, and adopting from it would restore a stale configuration — so when
// a per-machine manifest names the same machine, the root file is dropped from
// the result. It is left on the drive because moving or deleting it would stop
// an older backup-maker being able to adopt that destination at all, and
// because tidying up our own files at a destination root is exactly the kind of
// cleanup path this project does not add. The one residual is worth stating: an
// older binary reading such a drive still sees the fossil, and gets whatever
// configuration was current when it was last written.
func ListManifests(b localmirror.Backend) ([]FoundManifest, error) {
	var out []FoundManifest
	seen := map[string]bool{}

	dirs, err := localmirror.TopLevelDirs(b)
	if err != nil {
		return nil, err
	}
	for _, dir := range dirs {
		if dir == config.VersionsDirName || dir == archive.DirName {
			continue
		}
		m, err := readManifestAt(b, path.Join(dir, ManifestName))
		if err != nil || m == nil {
			continue // not a machine directory, or its manifest is unreadable
		}
		name := m.MachineName
		if name == "" {
			name = dir
		}
		seen[strings.ToLower(config.MachineDir(name))] = true
		out = append(out, FoundManifest{MachineName: name, Manifest: m})
	}

	if m, err := readManifestAt(b, ManifestName); err == nil && m != nil {
		if !seen[strings.ToLower(config.MachineDir(m.MachineName))] {
			out = append(out, FoundManifest{MachineName: m.MachineName, Manifest: m, Legacy: true})
		}
	}
	return out, nil
}

// ListManifestsAt is an os-path convenience for a locally mounted destination.
func ListManifestsAt(root string) ([]FoundManifest, error) {
	return ListManifests(localmirror.NewLocalFS(root))
}

// readManifestAt reads one manifest, returning (nil, nil) when there is simply
// no file there — the ordinary case for a directory that is not a machine's.
func readManifestAt(b localmirror.Backend, p string) (*Manifest, error) {
	data, err := b.ReadFile(p)
	if err != nil {
		return nil, nil
	}
	var m Manifest
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("corrupt backup manifest: %w", err)
	}
	return &m, nil
}

// ReadManifest loads a manifest from a destination: this destination's own
// machine directories first, the legacy root-level file as a fallback, and the
// most recently generated one when several are present and none was named.
//
// A version it does not recognise is READ ANYWAY rather than refused. A field a
// newer binary added and this one cannot see is not a reason to make a drive
// unadoptable — which, for a program whose entire purpose is getting data back,
// would be the worse failure by a long way.
func ReadManifest(b localmirror.Backend) (*Manifest, error) {
	found, err := ListManifests(b)
	if err != nil {
		return nil, err
	}
	if len(found) == 0 {
		return nil, fmt.Errorf("no backup manifest found on this destination")
	}
	best := found[0]
	for _, f := range found[1:] {
		if f.Manifest.Generated.After(best.Manifest.Generated) {
			best = f
		}
	}
	return best.Manifest, nil
}

// ReadManifestFor loads one named machine's manifest from a destination.
func ReadManifestFor(b localmirror.Backend, machineName string) (*Manifest, error) {
	found, err := ListManifests(b)
	if err != nil {
		return nil, err
	}
	for _, f := range found {
		if config.SameDest(f.MachineName, machineName) {
			return f.Manifest, nil
		}
	}
	return nil, fmt.Errorf("this destination holds no backups from a computer called %q", machineName)
}

// ReadManifestAt is an os-path convenience for adopting a locally mounted
// destination (drive, SD card, USB stick).
func ReadManifestAt(root string) (*Manifest, error) {
	return ReadManifest(localmirror.NewLocalFS(root))
}
