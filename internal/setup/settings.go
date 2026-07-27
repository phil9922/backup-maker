// SPDX-License-Identifier: MIT

package setup

import (
	"strings"

	"github.com/phil9922/backup-maker/internal/config"
)

// Settings is a change to [general], expressed as tri-state pointers: nil means
// "leave this one alone".
//
// A partial update rather than a whole-section overwrite, because the browser
// and the CLI both edit one thing at a time, and a struct of plain values would
// let a caller that forgot a field silently reset it. The keys the dashboard is
// never given — machine name, ports, the receive root — cannot be reached from
// here at all.
type Settings struct {
	DesktopAlerts       *bool
	BackupsStopped      *bool
	SnapshotFailed      *bool
	UnrecognisedStorage *bool
	PairRequests        *bool
	LANView             *bool
	// LANViewAccess is "all" or "approved"; empty leaves it unchanged.
	LANViewAccess string
	// WebhookEnabled and WebhookMinimal are the webhook delivery method.
	WebhookEnabled *bool
	WebhookMinimal *bool
	// NtfyEnabled and NtfyMinimal are the ntfy delivery method.
	NtfyEnabled *bool
	NtfyMinimal *bool
}

// ApplySettings writes the requested changes to config.toml. A running daemon
// picks them up within seconds, the same way every other setup call works.
//
// It returns whether the LAN view's listener needs restarting, which is the one
// change here that is not a matter of reading the config on the next cycle:
// that listener is bound at startup, so switching it on or off asks for more
// than a config reload.
func ApplySettings(s Settings) (lanViewChanged bool, err error) {
	cfg, err := config.Load()
	if err != nil {
		return false, err
	}
	before := cfg.General.LANView
	ApplySettingsTo(cfg, s)
	if err := cfg.Save(); err != nil {
		return false, err
	}
	return cfg.General.LANView != before, nil
}

// ApplySettingsTo applies a change to a config in memory. Separate so the same
// rules can be tested without touching the filesystem, and so a caller that
// already holds a config does not have to reload it.
func ApplySettingsTo(cfg *config.Config, s Settings) {
	if s.DesktopAlerts != nil {
		cfg.General.DesktopAlerts = *s.DesktopAlerts
	}
	if s.LANView != nil {
		cfg.General.LANView = *s.LANView
	}
	// Stored only when it is not the default, so a config file says nothing
	// about a choice the user never made.
	switch s.LANViewAccess {
	case "all":
		cfg.General.LANViewAccess = ""
	case "approved":
		cfg.General.LANViewAccess = "approved"
	}
	// Each category is stored only when it is being switched OFF. An "on" is
	// the default, so writing it back as an explicit true would litter every
	// config file with the settings nobody changed — and, worse, would freeze
	// today's defaults into files that outlive them.
	set := func(dst **bool, v *bool) {
		if v == nil {
			return
		}
		if *v {
			*dst = nil
			return
		}
		off := false
		*dst = &off
	}
	if s.WebhookEnabled != nil {
		cfg.General.Webhook.Enabled = *s.WebhookEnabled
	}
	if s.WebhookMinimal != nil {
		cfg.General.Webhook.Minimal = *s.WebhookMinimal
	}
	if s.NtfyEnabled != nil {
		cfg.General.Ntfy.Enabled = *s.NtfyEnabled
	}
	if s.NtfyMinimal != nil {
		cfg.General.Ntfy.Minimal = *s.NtfyMinimal
	}
	set(&cfg.General.Alerts.BackupsStopped, s.BackupsStopped)
	set(&cfg.General.Alerts.SnapshotFailed, s.SnapshotFailed)
	set(&cfg.General.Alerts.UnrecognisedStorage, s.UnrecognisedStorage)
	set(&cfg.General.Alerts.PairRequests, s.PairRequests)
}

// SetWebhookURL stores where alerts are POSTed.
//
// In state.json rather than config.toml, deliberately: a webhook URL is
// normally a credential — a Slack or Discord endpoint is a right to post, and
// an ntfy topic usually carries a token — and config.toml is the file this
// project treats as shareable. Same home as the share and archive passwords.
//
// An empty URL clears it, which is how the sink is un-configured without
// having to remember what was there.
func SetWebhookURL(url string) error {
	state, err := config.LoadState()
	if err != nil {
		return err
	}
	state.WebhookURL = strings.TrimSpace(url)
	return state.Save()
}

// SetNtfyTopicURL stores the ntfy topic alerts are published to.
//
// Beside the webhook URL in state.json, and for a sharper version of the same
// reason: on ntfy.sh the topic name is the whole of the access control, so it
// belongs with the passwords rather than in the file this project treats as
// shareable.
//
// An empty topic clears it, which is how the sink is un-configured.
func SetNtfyTopicURL(url string) error {
	state, err := config.LoadState()
	if err != nil {
		return err
	}
	state.NtfyTopicURL = strings.TrimSpace(url)
	return state.Save()
}

// SetNtfyToken stores the access token for a protected ntfy topic.
//
// SAVED SEPARATELY FROM THE TOPIC, deliberately. Neither is ever sent back to
// the dashboard, so a single control for both would mean that re-saving the
// topic with the token box empty — the only state the browser can be in, having
// never been told the token — silently threw the credential away.
//
// An empty token clears it, which is what a topic that stops being protected
// needs.
func SetNtfyToken(token string) error {
	state, err := config.LoadState()
	if err != nil {
		return err
	}
	state.NtfyToken = strings.TrimSpace(token)
	return state.Save()
}
