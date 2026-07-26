// SPDX-License-Identifier: MIT

package config

import (
	"encoding/json"
	"os"
	"time"
)

// State is machine-owned runtime data, distinct from the user-editable
// config.toml. The CLI reads it to find and authenticate to the daemon.
type State struct {
	// IPCToken authenticates CLI/dashboard requests to the daemon API.
	IPCToken string `json:"ipc_token"`
	// DashboardPort is the port the daemon actually bound (normally the
	// configured one).
	DashboardPort int `json:"dashboard_port,omitempty"`
	// SyncthingAPIKey and SyncthingGUIPort locate our private syncthing child.
	SyncthingAPIKey  string `json:"syncthing_api_key,omitempty"`
	SyncthingGUIPort int    `json:"syncthing_gui_port,omitempty"`
	// DriveTargetUUIDs maps drive/share target name -> UUID written into the
	// target's marker file, so different storage appearing at the same
	// location is refused.
	DriveTargetUUIDs map[string]string `json:"drive_target_uuids,omitempty"`
	// ShareCredentials maps share-target name -> SMB password. state.json is
	// 0600 and machine-owned: plaintext-but-private, the same trust level as
	// the IPC token and syncthing API key above.
	ShareCredentials map[string]string `json:"share_credentials,omitempty"`
	// AdvisorSeen records only that the setup-advisor quiz was offered once,
	// so it doesn't re-prompt. No quiz answers are ever stored.
	AdvisorSeen bool `json:"advisor_seen,omitempty"`
	// SetupComplete records that first-run setup was finished or deliberately
	// skipped, so the dashboard stops opening the wizard. It is only a
	// "don't ask again" marker: the wizard also stands down as soon as a
	// folder and a target exist, so a CLI-configured machine never sees it.
	SetupComplete bool `json:"setup_complete,omitempty"`
	// ArchivePasswords maps archive name -> the REQUIRED zip password (same
	// privacy level as ShareCredentials: 0600, machine-owned, never in
	// config.toml). Losing this password means losing access to the
	// archives; the wizard says so out loud.
	ArchivePasswords map[string]string `json:"archive_passwords,omitempty"`
	// ArchiveLastRun tracks when each archive job last completed, so
	// schedules survive daemon restarts and overdue jobs catch up.
	ArchiveLastRun map[string]time.Time `json:"archive_last_run,omitempty"`
}

func LoadState() (*State, error) {
	path, err := StatePath()
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return &State{}, nil
	}
	if err != nil {
		return nil, err
	}
	var s State
	if err := json.Unmarshal(data, &s); err != nil {
		return nil, err
	}
	return &s, nil
}

func (s *State) Save() error {
	path, err := StatePath()
	if err != nil {
		return err
	}
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
