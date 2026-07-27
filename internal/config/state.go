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
	// WebhookURL is where alerts are POSTed when the webhook sink is on.
	//
	// HERE RATHER THAN IN config.toml because it is usually a credential: a
	// Slack or Discord endpoint URL grants anyone holding it the right to post,
	// and an ntfy topic URL often carries a token. Same treatment as the share
	// and archive passwords two fields up.
	WebhookURL string `json:"webhook_url,omitempty"`
	// NtfyTopicURL is the ntfy topic alerts are published to, and NtfyToken the
	// access token for it when the topic is protected.
	//
	// BOTH HERE FOR THE SAME REASON AS WebhookURL, and the topic more urgently
	// than it looks: on ntfy.sh the topic name IS the access control, so a
	// config.toml carrying it could not be pasted into an issue report without
	// handing over every alert this machine sends.
	NtfyTopicURL string `json:"ntfy_topic_url,omitempty"`
	NtfyToken    string `json:"ntfy_token,omitempty"`
	// LANDevices remembers the browsers that have asked to read the read-only
	// network view, keyed by the token that view issued them.
	//
	// IN state.json, NOT config.toml, because the keys are credentials — the
	// same reason share passwords and the IPC token live here. The file is
	// 0600 and never leaves the machine.
	LANDevices map[string]*LANDevice `json:"lan_devices,omitempty"`
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
	// MirrorLastSync tracks when each folder last completed a sync to each
	// drive/share destination — folder id -> target name -> time — so those
	// clocks survive a daemon restart too. Without it a reboot resets every
	// destination to "never synced", and a drive that has been unplugged for
	// months reads as merely offline rather than stale. Paired machines need no
	// entry here: their last-seen comes from the sync engine's own on-disk
	// statistics, which already survive restarts.
	//
	// Nested rather than one joined key, because target names are free-form text
	// from config.toml and a hand-edited folder id can be anything too: there is
	// no delimiter both are guaranteed not to contain, and a collision here
	// would hand one destination another's clock.
	//
	// The daemon batches writes to this alongside the counters below (see
	// internal/daemon/tally.go): a busy folder syncs every few seconds, and
	// state.json is not a log.
	MirrorLastSync map[string]map[string]time.Time `json:"mirror_last_sync,omitempty"`
	// BytesCopiedTotal and FilesCopiedTotal are the lifetime odometer: every
	// byte and file backup-maker has itself written to a destination since
	// CountingSince, re-copies of a changed file included. It lives here rather
	// than in config.toml because it is machine-owned bookkeeping, and here
	// rather than on the destinations because it is a fact about this
	// installation's work, not about any one drive.
	//
	// It counts ONLY what this program copies: the mirror engine's drive and
	// share targets, and the snapshot writer. Data sent to a paired machine
	// travels through the sync engine and is not counted — see status.Totals,
	// which carries the target mix so nothing ever renders this as a bare
	// number without saying what it covers.
	//
	// The daemon batches writes to these (see internal/daemon/tally.go): losing
	// a few seconds of counter to a crash is fine, rewriting state.json once
	// per copied file is not.
	BytesCopiedTotal uint64    `json:"bytes_copied_total,omitempty"`
	FilesCopiedTotal uint64    `json:"files_copied_total,omitempty"`
	CountingSince    time.Time `json:"counting_since,omitzero"`
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

// LANDevice is one browser that has asked to read the network view.
//
// Approval is per BROWSER, not per machine or per address: a token in a cookie
// is the only thing on a LAN that is actually hard to forge. The consequence is
// honest and worth stating in the UI — clearing cookies, or opening the view in
// a different browser, asks again.
type LANDevice struct {
	// Code is a short human-readable string shown on both screens, so the
	// person approving can tell WHICH device they are approving. Without it,
	// approving from a list of anonymous entries is a coin toss.
	Code string `json:"code"`
	// Approved is the whole gate. A device that has never been approved reads
	// the holding page and nothing else.
	Approved  bool      `json:"approved,omitempty"`
	FirstSeen time.Time `json:"first_seen,omitzero"`
	LastSeen  time.Time `json:"last_seen,omitzero"`
	// Addr is the address it last connected from, purely so a person can
	// recognise it. Never used to decide anything.
	Addr string `json:"addr,omitempty"`
	// Agent is a coarse guess at the kind of device ("iPhone", "Android"),
	// for the same recognition purpose. Never trusted.
	Agent string `json:"agent,omitempty"`
}
