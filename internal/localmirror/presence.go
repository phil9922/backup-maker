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
}

// WriteMarker initializes a backend root as a backup target.
func WriteMarker(b Backend, uuid, machineName string) error {
	m := Marker{TargetUUID: uuid, MachineName: machineName, Created: time.Now()}
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
