// SPDX-License-Identifier: MIT

package localmirror

import (
	"encoding/json"
	"fmt"
	"time"
)

// MarkerName identifies a prepared backup target. Its UUID protects against a
// different drive/share appearing at the same location: we refuse to write
// rather than mirror onto a stranger's storage.
const MarkerName = ".backup-maker-target.json"

type Marker struct {
	TargetUUID  string    `json:"target_uuid"`
	MachineName string    `json:"machine_name"`
	Created     time.Time `json:"created"`
	// Description is which physical drive this is, in the user's own words
	// ("Samsung 128GB card, lives in the laptop"). Optional and purely for
	// humans: nothing branches on it.
	//
	// ON THE STORAGE RATHER THAN IN config.toml, which is the whole point. The
	// volume label cannot be read over SMB — go-smb2 exposes no decoder for
	// FileFsVolumeInformation — so there is no way to learn what a share is
	// sitting on, and two identical cards are indistinguishable once they are
	// out of the machine. Written here, the answer is on the thing itself: it
	// survives a reinstall, and adopt can say which drive it is looking at.
	Description string `json:"description,omitempty"`
}

// WriteMarker initializes a backend root as a backup target.
func WriteMarker(b Backend, uuid, machineName string) error {
	return WriteMarkerDescribed(b, uuid, machineName, "")
}

// WriteMarkerDescribed is WriteMarker carrying a human description of the drive.
func WriteMarkerDescribed(b Backend, uuid, machineName, description string) error {
	m := Marker{TargetUUID: uuid, MachineName: machineName, Created: time.Now(),
		Description: description}
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	return b.WriteFile(MarkerName, data)
}

// Describe records what a drive is, keeping everything else in its marker.
//
// READ-MODIFY-WRITE, and the identity it preserves is the point: the UUID in
// this file is what says the storage at a mount point is still the storage this
// target was set up against. Writing a fresh marker to add a sentence would
// replace that UUID, and the destination would then be refused as foreign
// storage — the drive is fine and every backup on it stops.
func Describe(b Backend, description string) error {
	m, err := ReadMarker(b)
	if err != nil {
		return err
	}
	m.Description = description
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	return b.WriteFile(MarkerName, data)
}

// ReadMarker loads a target's marker, if present.
func ReadMarker(b Backend) (*Marker, error) {
	data, err := b.ReadFile(MarkerName)
	if err != nil {
		return nil, err
	}
	var m Marker
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("corrupt target marker: %w", err)
	}
	return &m, nil
}

// ReadMarkerAt is an os-path convenience for setup commands.
func ReadMarkerAt(root string) (*Marker, error) {
	return ReadMarker(NewLocalFS(root))
}

// WriteMarkerAt is an os-path convenience for setup commands.
func WriteMarkerAt(root, uuid, machineName string) error {
	return WriteMarker(NewLocalFS(root), uuid, machineName)
}

// presence classifies the target's current state for one engine.
type presence int

const (
	presentOK presence = iota
	absent
	wrongDrive // marker exists but UUID mismatches: never write
)

func checkPresence(b Backend, wantUUID string) presence {
	m, err := ReadMarker(b)
	if err != nil {
		return absent
	}
	if m.TargetUUID != wantUUID {
		return wrongDrive
	}
	return presentOK
}

// Recognition is what the storage currently at a target's location turns out to
// be.
type Recognition int

const (
	// Ours: the marker is there and carries the UUID recorded for this target.
	// The ONLY state in which anything of ours may be written.
	Ours Recognition = iota
	// Offline: the location cannot be read at all — a drive unplugged, a share
	// asleep. Ordinary, and not worth a word.
	Offline
	// Foreign: something IS at that location and it is not our target, either
	// because the marker is missing (a reformatted card) or because it belongs
	// to different storage (a stranger's stick at the same mount point).
	Foreign
)

// Recognize classifies the storage at b's root against the UUID recorded for
// the target, and is the one question every writer must ask before putting a
// byte on a destination.
//
// It exists so callers outside this package hold themselves to exactly the rule
// the mirror applies before copying: Ours is what makes sync() proceed, and
// nothing else. Anything we leave at a destination root — the adoption manifest,
// the status page — describes this machine, so a destination we refuse to back
// up to must not receive those either. The Offline/Foreign split changes only
// what a caller can SAY about the refusal, never whether it refuses.
func Recognize(b Backend, wantUUID string) Recognition {
	switch checkPresence(b, wantUUID) {
	case presentOK:
		return Ours
	case wrongDrive:
		return Foreign
	}
	// No readable marker: either nothing is mounted at that location, or
	// something is and it isn't ours.
	if _, err := b.Stat(""); err != nil {
		return Offline
	}
	return Foreign
}
