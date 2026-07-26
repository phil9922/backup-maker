// SPDX-License-Identifier: MIT

package setup

import (
	"time"

	"github.com/phil9922/backup-maker/internal/browse"
)

// AdoptCandidate is an attached drive that carries an adoption manifest —
// something "backup-maker adopt" (CLI or browser) can restore a machine from.
type AdoptCandidate struct {
	Path        string    `json:"path"`
	MachineName string    `json:"machine_name"`
	Folders     int       `json:"folders"`
	Targets     int       `json:"targets"`
	Archives    int       `json:"archives"`
	Generated   time.Time `json:"generated"`
}

// ScanForManifests checks every mounted volume browse.Drives can see for an
// adoption manifest at its root. It stats exactly one filename per volume —
// no recursion — so it is safe to call on every render of a setup screen.
// Network shares can't be scanned (they need credentials); those are always
// pointed at explicitly.
func ScanForManifests() []AdoptCandidate {
	var roots []string
	for _, d := range browse.Drives() {
		roots = append(roots, d.Path)
	}
	return scanRoots(roots)
}

// scanRoots probes each root for a manifest. Split from ScanForManifests so
// tests can point it at temp directories instead of real mounts.
func scanRoots(roots []string) []AdoptCandidate {
	var out []AdoptCandidate
	for _, root := range roots {
		m, err := ReadManifestAt(root)
		if err != nil {
			continue
		}
		out = append(out, AdoptCandidate{
			Path:        root,
			MachineName: m.MachineName,
			Folders:     len(m.Folders),
			Targets:     len(m.Targets),
			Archives:    len(m.Archives),
			Generated:   m.Generated,
		})
	}
	return out
}
