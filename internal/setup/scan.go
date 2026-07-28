// SPDX-License-Identifier: MIT

package setup

import (
	"os"
	"path/filepath"
	"time"

	"github.com/phil9922/backup-maker/internal/archive"
	"github.com/phil9922/backup-maker/internal/browse"
	"github.com/phil9922/backup-maker/internal/config"
)

// AdoptCandidate is one machine's backups found on an attached drive —
// something "backup-maker adopt" (CLI or browser) can restore a machine from.
type AdoptCandidate struct {
	Path        string    `json:"path"`
	MachineName string    `json:"machine_name"`
	Folders     int       `json:"folders"`
	Targets     int       `json:"targets"`
	Archives    int       `json:"archives"`
	Generated   time.Time `json:"generated"`
}

// ScanForManifests checks every mounted volume browse.Drives can see for
// adoptable backups. Network shares can't be scanned (they need credentials);
// those are always pointed at explicitly.
func ScanForManifests() []AdoptCandidate {
	var roots []string
	for _, d := range browse.Drives() {
		roots = append(roots, d.Path)
	}
	return scanRoots(roots)
}

// scanRoots probes each root for adoptable backups, and each root's immediate
// subdirectories too. Split from ScanForManifests so tests can point it at temp
// directories instead of real mounts.
//
// ONE LEVEL DOWN AS WELL AS THE ROOT, because a destination no longer has to be
// a whole drive: somebody who keeps backups in a folder on a drive they use for
// other things would otherwise find that "restore this machine" cannot see
// their backups at all — the one screen where being invisible costs the most.
//
// The cost stays bounded and this stays safe to call on every render of a setup
// screen: one directory read per volume, then a fixed number of stats per
// entry, and no recursion below that.
func scanRoots(roots []string) []AdoptCandidate {
	var out []AdoptCandidate
	for _, root := range roots {
		out = append(out, candidatesAt(root)...)
		for _, sub := range subdirsOf(root) {
			out = append(out, candidatesAt(sub)...)
		}
	}
	return out
}

// candidatesAt lists every machine with backups at one destination root.
func candidatesAt(root string) []AdoptCandidate {
	found, err := ListManifestsAt(root)
	if err != nil {
		return nil
	}
	out := make([]AdoptCandidate, 0, len(found))
	for _, f := range found {
		out = append(out, AdoptCandidate{
			Path:        root,
			MachineName: f.MachineName,
			Folders:     len(f.Manifest.Folders),
			Targets:     len(f.Manifest.Targets),
			Archives:    len(f.Manifest.Archives),
			Generated:   f.Manifest.Generated,
		})
	}
	return out
}

// subdirsOf lists a drive's immediate subdirectories, skipping the ones we
// already know are ours — a machine directory, the version store and the
// snapshot store are never themselves destination roots, and descending into
// them would report the same backups twice under a path that is not a target.
func subdirsOf(root string) []string {
	des, err := os.ReadDir(root)
	if err != nil {
		return nil
	}
	var out []string
	for _, de := range des {
		if !de.IsDir() {
			continue
		}
		name := de.Name()
		if name == config.VersionsDirName || name == archive.DirName {
			continue
		}
		// A machine directory has our manifest in it; a folder someone keeps
		// backups in has our marker at ITS root. Telling them apart this way
		// costs one stat and stops the same backups being listed twice.
		if _, err := os.Stat(filepath.Join(root, name, ManifestName)); err == nil {
			continue
		}
		out = append(out, filepath.Join(root, name))
	}
	return out
}
