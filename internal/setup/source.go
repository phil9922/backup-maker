// SPDX-License-Identifier: MIT

package setup

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/phil9922/backup-maker/internal/localmirror"
	"github.com/phil9922/backup-maker/internal/smbfs"
)

// AdoptSource locates the destination adoption reads from: either a locally
// mounted directory (Path) or an SMB share (URL plus credentials). Shared by
// the CLI and the dashboard API.
type AdoptSource struct {
	Path     string `json:"path,omitempty"`
	URL      string `json:"url,omitempty"`
	Username string `json:"username,omitempty"`
	Password string `json:"password,omitempty"`
}

// open connects to the source. The caller must Close the backend.
func (s AdoptSource) open() (localmirror.Backend, error) {
	if s.URL != "" {
		return smbfs.New(s.URL, s.Username, s.Password)
	}
	if s.Path == "" {
		return nil, errors.New("no destination given")
	}
	abs, err := filepath.Abs(ExpandHome(s.Path))
	if err != nil {
		return nil, fmt.Errorf("bad path %q: %w", s.Path, err)
	}
	if fi, err := os.Stat(abs); err != nil || !fi.IsDir() {
		return nil, fmt.Errorf("not a readable directory: %s", abs)
	}
	return localmirror.NewLocalFS(abs), nil
}

// AdoptInspection is what a destination reveals before any decision is made:
// the manifest's contents plus what THIS machine can add — whether each source
// folder exists here, and which target the inspected destination itself is.
type AdoptInspection struct {
	MachineName string             `json:"machine_name"`
	Generated   time.Time          `json:"generated"`
	Folders     []AdoptFolderInfo  `json:"folders"`
	Targets     []AdoptTargetInfo  `json:"targets"`
	Archives    []AdoptArchiveInfo `json:"archives"`
}

type AdoptFolderInfo struct {
	ID    string `json:"id"`
	Path  string `json:"path"`
	Label string `json:"label"`
	// Exists reports whether Path is a directory on THIS machine — false means
	// the flow should offer a remap (new username, different OS).
	Exists bool `json:"exists"`
}

type AdoptTargetInfo struct {
	Name string `json:"name"`
	Type string `json:"type"`
	// Location is the drive path or share URL — whichever the type uses.
	Location string `json:"location"`
	Username string `json:"username,omitempty"`
	// PointedAt marks the target the inspected destination itself is (matched
	// by marker UUID, or by URL for shares): its credentials are already in
	// hand and must not be asked for again.
	PointedAt bool `json:"pointed_at"`
	// HasUUID is false for a drive/share the manifest carries no UUID for —
	// the daemon won't recognize it until it is re-added.
	HasUUID bool `json:"has_uuid"`
}

type AdoptArchiveInfo struct {
	Name   string `json:"name"`
	Every  string `json:"every"`
	Target string `json:"target"`
}

// InspectSource reads a destination's manifest and reports what adopting it
// would restore. It does not write anything anywhere.
func InspectSource(src AdoptSource) (*AdoptInspection, error) {
	b, err := src.open()
	if err != nil {
		return nil, err
	}
	defer b.Close()
	m, err := ReadManifest(b)
	if err != nil {
		return nil, fmt.Errorf("no adoptable backup found there: %w", err)
	}
	pointed := pointedTargetName(b, m, src)

	insp := &AdoptInspection{MachineName: m.MachineName, Generated: m.Generated}
	for _, f := range m.Folders {
		fi, statErr := os.Stat(f.Path)
		insp.Folders = append(insp.Folders, AdoptFolderInfo{
			ID: f.ID, Path: f.Path, Label: f.Label,
			Exists: statErr == nil && fi.IsDir(),
		})
	}
	for _, mt := range m.Targets {
		loc := mt.Path
		if mt.Type == "share" {
			loc = mt.URL
		}
		insp.Targets = append(insp.Targets, AdoptTargetInfo{
			Name: mt.Name, Type: mt.Type, Location: loc, Username: mt.Username,
			PointedAt: mt.Name == pointed,
			HasUUID:   mt.Type == "device" || mt.UUID != "",
		})
	}
	for _, a := range m.Archives {
		insp.Archives = append(insp.Archives, AdoptArchiveInfo{
			Name: a.Name, Every: a.Every, Target: a.Target,
		})
	}
	return insp, nil
}

// AdoptFromSource re-reads the manifest from the source and adopts it. When
// the source is a share that matches one of the manifest's targets, the
// credentials used to reach it are stored for that target automatically — the
// user already proved they hold them.
func AdoptFromSource(src AdoptSource, dec AdoptDecisions) (*AdoptResult, error) {
	b, err := src.open()
	if err != nil {
		return nil, err
	}
	defer b.Close()
	m, err := ReadManifest(b)
	if err != nil {
		return nil, fmt.Errorf("no adoptable backup found there: %w", err)
	}
	// The credentials that just read the manifest are proven to work — store
	// them for the pointed-at share even when empty (a guest share).
	if pointed := pointedTargetName(b, m, src); pointed != "" && src.URL != "" {
		if dec.SharePasswords == nil {
			dec.SharePasswords = map[string]string{}
		}
		if _, exists := dec.SharePasswords[pointed]; !exists {
			dec.SharePasswords[pointed] = src.Password
		}
	}
	return Adopt(m, dec)
}

// pointedTargetName identifies which manifest target the source destination
// is: by marker UUID when a marker is readable, else (for shares) by URL.
func pointedTargetName(b localmirror.Backend, m *Manifest, src AdoptSource) string {
	if marker, err := localmirror.ReadMarker(b); err == nil {
		for _, mt := range m.Targets {
			if mt.UUID != "" && mt.UUID == marker.TargetUUID {
				return mt.Name
			}
		}
	}
	if src.URL != "" {
		for _, mt := range m.Targets {
			if mt.Type == "share" && mt.URL == src.URL {
				return mt.Name
			}
		}
	}
	return ""
}
