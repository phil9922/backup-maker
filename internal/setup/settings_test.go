// SPDX-License-Identifier: MIT

package setup

import (
	"os"
	"strings"
	"testing"

	"github.com/phil9922/backup-maker/internal/config"
)

func yes() *bool { v := true; return &v }
func no() *bool  { v := false; return &v }

// A partial update leaves everything it did not mention alone. This is the
// whole reason Settings is a struct of pointers: the dashboard sends one field
// per click, and a shape that could not tell "unchecked" from "not sent" would
// switch off every alert the user did not happen to touch.
func TestApplyingOneSettingLeavesTheRestAlone(t *testing.T) {
	cfg := config.New()
	cfg.General.DesktopAlerts = true

	ApplySettingsTo(cfg, Settings{SnapshotFailed: no()})

	if !cfg.General.DesktopAlerts {
		t.Error("changing a category switched off desktop alerts entirely")
	}
	a := cfg.General.Alerts
	if a.SnapshotFailedOn() {
		t.Error("the category that was sent was not switched off")
	}
	if !a.BackupsStoppedOn() || !a.UnrecognisedStorageOn() || !a.PairRequestsOn() {
		t.Error("categories that were not mentioned were switched off too")
	}
	if cfg.General.LANView {
		t.Error("the network view was switched on by an unrelated change")
	}
}

// Switching a category back ON stores nothing rather than an explicit true.
// A file full of `= true` lines freezes today's defaults into configs that
// outlive them: if a future version decides a category should behave
// differently by default, every user who ever clicked it would be pinned.
func TestSwitchingACategoryBackOnLeavesNoResidue(t *testing.T) {
	cfg := config.New()
	ApplySettingsTo(cfg, Settings{PairRequests: no()})
	if cfg.General.Alerts.PairRequests == nil {
		t.Fatal("switching off stored nothing")
	}
	ApplySettingsTo(cfg, Settings{PairRequests: yes()})
	if cfg.General.Alerts.PairRequests != nil {
		t.Error("switching back on stored an explicit true instead of clearing the override")
	}
	if !cfg.General.Alerts.PairRequestsOn() {
		t.Error("the category did not come back on")
	}
}

// The network view is the one setting here that changes what this machine
// exposes to other people, so it gets its own check in both directions.
func TestTheNetworkViewCanBeSwitchedBothWays(t *testing.T) {
	cfg := config.New()
	ApplySettingsTo(cfg, Settings{LANView: yes()})
	if !cfg.General.LANView {
		t.Fatal("the network view could not be switched on")
	}
	ApplySettingsTo(cfg, Settings{LANView: no()})
	if cfg.General.LANView {
		t.Error("the network view could not be switched off — an unauthenticated listener the user asked to stop")
	}
}

// And the alert settings survive the round trip through the file the daemon
// actually reads, not just through a struct in memory.
func TestSettingsSurviveSavingAndLoading(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	t.Setenv("HOME", dir)
	t.Setenv("AppData", dir)
	t.Setenv("LOCALAPPDATA", dir)

	cfg := config.New()
	cfg.Folders = nil
	if err := cfg.Save(); err != nil {
		t.Fatalf("seeding config: %v", err)
	}

	if _, err := ApplySettings(Settings{DesktopAlerts: no(), UnrecognisedStorage: no()}); err != nil {
		t.Fatalf("applying: %v", err)
	}
	reloaded, err := config.Load()
	if err != nil {
		t.Fatalf("reloading: %v", err)
	}
	if reloaded.General.DesktopAlerts {
		t.Error("desktop_alerts = false did not survive the file")
	}
	if reloaded.General.Alerts.UnrecognisedStorageOn() {
		t.Error("the silenced category did not survive the file")
	}
	if !reloaded.General.Alerts.BackupsStoppedOn() {
		t.Error("an untouched category came back off")
	}
}

// The webhook address is a credential — a Slack endpoint is a right to post,
// an ntfy topic usually carries a token — so it belongs in state.json beside
// the share passwords, NOT in the config file this project calls shareable.
func TestTheWebhookAddressIsKeptOutOfTheConfigFile(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	t.Setenv("HOME", dir)
	t.Setenv("AppData", dir)
	t.Setenv("LOCALAPPDATA", dir)

	cfg := config.New()
	cfg.Folders = nil
	if err := cfg.Save(); err != nil {
		t.Fatal(err)
	}
	const secret = "https://hooks.example.com/T00/B11/xoxb-very-secret"
	if err := SetWebhookURL(secret); err != nil {
		t.Fatalf("saving: %v", err)
	}

	// It must be readable back by the daemon...
	state, err := config.LoadState()
	if err != nil {
		t.Fatal(err)
	}
	if state.WebhookURL != secret {
		t.Errorf("the address did not survive: %q", state.WebhookURL)
	}
	// ...and absent from config.toml entirely.
	path, err := config.ConfigPath()
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "xoxb-very-secret") || strings.Contains(string(raw), "hooks.example.com") {
		t.Errorf("the webhook credential was written to the shareable config file:\n%s", raw)
	}
}

// Switching the webhook on is independent of the desktop popup: both can be on
// at once, and turning one on must never turn the other off.
func TestDeliveryMethodsAreIndependentOfEachOther(t *testing.T) {
	cfg := config.New()
	cfg.General.DesktopAlerts = true

	ApplySettingsTo(cfg, Settings{WebhookEnabled: yes()})
	if !cfg.General.Webhook.Enabled {
		t.Error("the webhook did not switch on")
	}
	if !cfg.General.DesktopAlerts {
		t.Error("switching the webhook on switched desktop notifications off")
	}

	ApplySettingsTo(cfg, Settings{NtfyEnabled: yes()})
	if !cfg.General.Ntfy.Enabled {
		t.Error("ntfy did not switch on")
	}
	if !cfg.General.Webhook.Enabled || !cfg.General.DesktopAlerts {
		t.Error("switching ntfy on disturbed a method that was already running")
	}

	ApplySettingsTo(cfg, Settings{DesktopAlerts: no()})
	if cfg.General.DesktopAlerts {
		t.Error("desktop notifications did not switch off")
	}
	if !cfg.General.Webhook.Enabled {
		t.Error("switching the desktop popup off took the webhook with it")
	}
	if !cfg.General.Ntfy.Enabled {
		t.Error("switching the desktop popup off took ntfy with it")
	}
}

// The ntfy topic gets the webhook address's treatment, and needs it MORE: on
// the public server the topic name is the entire access control, so a topic in
// config.toml would mean a file nobody could paste into an issue report without
// handing over every alert the machine sends.
func TestTheNtfyTopicAndTokenAreKeptOutOfTheConfigFile(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	t.Setenv("HOME", dir)
	t.Setenv("AppData", dir)
	t.Setenv("LOCALAPPDATA", dir)

	cfg := config.New()
	cfg.Folders = nil
	if err := cfg.Save(); err != nil {
		t.Fatal(err)
	}
	const topic = "https://ntfy.sh/backups-9f3k2n-private"
	const token = "tk_averysecrettoken"
	if err := SetNtfyTopicURL(topic); err != nil {
		t.Fatalf("saving the topic: %v", err)
	}
	if err := SetNtfyToken(token); err != nil {
		t.Fatalf("saving the token: %v", err)
	}

	state, err := config.LoadState()
	if err != nil {
		t.Fatal(err)
	}
	if state.NtfyTopicURL != topic {
		t.Errorf("the topic did not survive: %q", state.NtfyTopicURL)
	}
	if state.NtfyToken != token {
		t.Errorf("the token did not survive: %q", state.NtfyToken)
	}

	path, err := config.ConfigPath()
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"backups-9f3k2n-private", "averysecrettoken", "ntfy.sh"} {
		if strings.Contains(string(raw), secret) {
			t.Errorf("%q was written to the shareable config file:\n%s", secret, raw)
		}
	}
}

// The token saves on its own. THE BUG THIS FORBIDS: the browser is never told
// the token, so if saving a topic also wrote the token field, editing the topic
// would post an empty token box and silently throw the credential away — and
// the next alert would be refused by a server that had been working.
func TestSavingTheNtfyTopicLeavesTheTokenAlone(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	t.Setenv("HOME", dir)
	t.Setenv("AppData", dir)
	t.Setenv("LOCALAPPDATA", dir)

	cfg := config.New()
	cfg.Folders = nil
	if err := cfg.Save(); err != nil {
		t.Fatal(err)
	}
	if err := SetNtfyToken("tk_keepme"); err != nil {
		t.Fatal(err)
	}
	if err := SetNtfyTopicURL("https://ntfy.sh/second-topic"); err != nil {
		t.Fatal(err)
	}
	state, err := config.LoadState()
	if err != nil {
		t.Fatal(err)
	}
	if state.NtfyToken != "tk_keepme" {
		t.Errorf("the token was lost when the topic was saved: %q", state.NtfyToken)
	}
	// And an empty save really does clear it, which is how a topic that stops
	// being protected is un-configured.
	if err := SetNtfyToken(""); err != nil {
		t.Fatal(err)
	}
	if state, _ := config.LoadState(); state.NtfyToken != "" {
		t.Error("saving an empty token did not remove it")
	}
}

// Off by default with nothing configured, like every other delivery method: a
// fresh install publishes to nobody's server.
func TestAFreshInstallHasNoNtfy(t *testing.T) {
	cfg := config.New()
	if cfg.General.Ntfy.Enabled {
		t.Error("ntfy is on by default")
	}
	if cfg.General.Ntfy.Minimal {
		t.Error("minimal mode defaults on, which would silently reduce an ntfy nobody configured")
	}
}

// Off by default, and nothing configured: a fresh install sends nothing
// anywhere, which is the promise the whole program rests on.
func TestAFreshInstallHasNoWebhook(t *testing.T) {
	cfg := config.New()
	if cfg.General.Webhook.Enabled {
		t.Error("the webhook is on by default")
	}
	if cfg.General.Webhook.Minimal {
		t.Error("minimal mode defaults on, which would silently reduce a webhook nobody configured")
	}
}
