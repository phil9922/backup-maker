// SPDX-License-Identifier: MIT

package setup

import (
	"testing"
	"time"

	"github.com/phil9922/backup-maker/internal/config"
	"github.com/phil9922/backup-maker/internal/localmirror"
)

// isolateNoConfig sandboxes config at a throwaway dir WITHOUT creating a
// config.toml, so config.Exists() is false — the state a fresh install adopts
// from.
func isolateNoConfig(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir) // linux
	t.Setenv("HOME", dir)            // macOS
}

// sampleConfig is a small but complete machine config: one folder, a drive and
// a share destination, and a scheduled archive.
func sampleConfig() *config.Config {
	cfg := config.New()
	cfg.General.MachineName = "oldbox"
	cfg.Folders = []config.Folder{
		{ID: "aaaaa-bbbbb", Path: "/home/old/docs", Label: "docs"},
	}
	cfg.Targets = []config.Target{
		{Type: "drive", Name: "usb", Path: "/mnt/usb", Folders: []string{"aaaaa-bbbbb"}},
		{Type: "share", Name: "nas", URL: "//nas/backups", Username: "phil", Folders: []string{}},
	}
	cfg.Archives = []config.Archive{
		{Name: "weekly", Every: "weekly", Target: "usb", Keep: 5},
	}
	return cfg
}

func sampleManifest() *Manifest {
	m := BuildManifest(sampleConfig(), map[string]string{"usb": "uuid-usb", "nas": "uuid-nas"}, "install-oldbox", time.Now())
	return &m
}

func TestManifestRoundTrip(t *testing.T) {
	b := localmirror.NewLocalFS(t.TempDir())
	uuids := map[string]string{"usb": "uuid-usb", "nas": "uuid-nas"}
	if err := WriteManifest(b, sampleConfig(), uuids, "install-oldbox"); err != nil {
		t.Fatalf("WriteManifest: %v", err)
	}
	m, err := ReadManifest(b)
	if err != nil {
		t.Fatalf("ReadManifest: %v", err)
	}
	if m.Version != ManifestVersion {
		t.Errorf("version = %d, want %d", m.Version, ManifestVersion)
	}
	if m.MachineName != "oldbox" {
		t.Errorf("machine = %q, want oldbox", m.MachineName)
	}
	if len(m.Folders) != 1 || len(m.Targets) != 2 || len(m.Archives) != 1 {
		t.Fatalf("counts: %d folders, %d targets, %d archives", len(m.Folders), len(m.Targets), len(m.Archives))
	}
	if m.Targets[0].UUID != "uuid-usb" || m.Targets[1].UUID != "uuid-nas" {
		t.Errorf("uuids not round-tripped: %q, %q", m.Targets[0].UUID, m.Targets[1].UUID)
	}
	if m.Targets[1].Username != "phil" {
		t.Errorf("username = %q, want phil", m.Targets[1].Username)
	}
}

func TestAdoptContinueAsMachine(t *testing.T) {
	isolateNoConfig(t)
	res, err := Adopt(sampleManifest(), AdoptDecisions{
		ContinueAsMachine: true,
		SharePasswords:    map[string]string{"nas": "sharepw"},
		ArchivePasswords:  map[string]string{"weekly": "zippw"},
	})
	if err != nil {
		t.Fatalf("Adopt: %v", err)
	}
	if res.MachineName != "oldbox" {
		t.Errorf("machine = %q, want oldbox", res.MachineName)
	}

	cfg := load(t)
	if cfg.General.MachineName != "oldbox" {
		t.Errorf("saved machine = %q, want oldbox", cfg.General.MachineName)
	}
	if cfg.Folders[0].ID != "aaaaa-bbbbb" {
		t.Errorf("folder id = %q, want it PRESERVED as aaaaa-bbbbb", cfg.Folders[0].ID)
	}

	st, err := config.LoadState()
	if err != nil {
		t.Fatal(err)
	}
	if st.DriveTargetUUIDs["usb"] != "uuid-usb" || st.DriveTargetUUIDs["nas"] != "uuid-nas" {
		t.Errorf("uuids not recorded: %+v", st.DriveTargetUUIDs)
	}
	if st.ShareCredentials["nas"] != "sharepw" {
		t.Errorf("share password not stored: %q", st.ShareCredentials["nas"])
	}
	if st.ArchivePasswords["weekly"] != "zippw" {
		t.Errorf("archive password not stored: %q", st.ArchivePasswords["weekly"])
	}
	if !st.SetupComplete {
		t.Error("SetupComplete should be true after adopt")
	}
}

func TestAdoptNewMachineRemapsFolderIDs(t *testing.T) {
	isolateNoConfig(t)
	if _, err := Adopt(sampleManifest(), AdoptDecisions{
		ContinueAsMachine: false,
		NewMachineName:    "newbox",
	}); err != nil {
		t.Fatalf("Adopt: %v", err)
	}

	cfg := load(t)
	if cfg.General.MachineName != "newbox" {
		t.Errorf("machine = %q, want newbox", cfg.General.MachineName)
	}
	newID := cfg.Folders[0].ID
	if newID == "aaaaa-bbbbb" {
		t.Error("folder id should have been re-minted for a new machine")
	}
	// The target's folder reference must follow the re-minted ID, or the config
	// would be internally inconsistent (and Validate would have rejected it).
	var usb config.Target
	for _, tg := range cfg.Targets {
		if tg.Name == "usb" {
			usb = tg
		}
	}
	if len(usb.Folders) != 1 || usb.Folders[0] != newID {
		t.Errorf("usb.Folders = %v, want [%s]", usb.Folders, newID)
	}
}

func TestAdoptPathRemap(t *testing.T) {
	isolateNoConfig(t)
	if _, err := Adopt(sampleManifest(), AdoptDecisions{
		ContinueAsMachine: true,
		PathRemap:         map[string]string{"aaaaa-bbbbb": "/home/new/docs"},
	}); err != nil {
		t.Fatalf("Adopt: %v", err)
	}
	cfg := load(t)
	if cfg.Folders[0].Path != "/home/new/docs" {
		t.Errorf("path = %q, want remapped /home/new/docs", cfg.Folders[0].Path)
	}
}

func TestAdoptRefusesWhenAlreadyConfigured(t *testing.T) {
	isolate(t) // creates a config.toml
	cfg := load(t)
	cfg.Folders = []config.Folder{{ID: "zzzzz-zzzzz", Path: "/tmp", Label: "x"}}
	if err := cfg.Save(); err != nil {
		t.Fatal(err)
	}
	if _, err := Adopt(sampleManifest(), AdoptDecisions{ContinueAsMachine: true}); err == nil {
		t.Fatal("expected adopt to refuse when folders are already configured")
	}
}

func TestAdoptAllowedOnEmptyInitedConfig(t *testing.T) {
	// backup-maker init creates a config.toml before the daemon (and so the
	// browser) can run: an EXISTING but EMPTY config must still be adoptable.
	isolate(t) // creates an empty config.toml, like init
	if err := AdoptAllowed(); err != nil {
		t.Fatalf("AdoptAllowed on empty config: %v", err)
	}
	if _, err := Adopt(sampleManifest(), AdoptDecisions{ContinueAsMachine: true}); err != nil {
		t.Fatalf("Adopt over empty init'ed config: %v", err)
	}
	cfg := load(t)
	if cfg.General.MachineName != "oldbox" {
		t.Errorf("machine = %q, want oldbox", cfg.General.MachineName)
	}
}

func TestAdoptPreservesGeneralSettings(t *testing.T) {
	// The daemon may be live on its configured dashboard port; adoption must
	// only change the machine name, never the rest of [general].
	isolate(t)
	cfg := load(t)
	cfg.General.DashboardPort = 9999
	if err := cfg.Save(); err != nil {
		t.Fatal(err)
	}
	if _, err := Adopt(sampleManifest(), AdoptDecisions{ContinueAsMachine: true}); err != nil {
		t.Fatalf("Adopt: %v", err)
	}
	after := load(t)
	if after.General.DashboardPort != 9999 {
		t.Errorf("dashboard port = %d, want preserved 9999", after.General.DashboardPort)
	}
	if after.General.MachineName != "oldbox" {
		t.Errorf("machine = %q, want oldbox", after.General.MachineName)
	}
}

func TestAdoptReportsMissingUUIDsAndPasswords(t *testing.T) {
	isolateNoConfig(t)
	// Manifest whose share target has no recorded UUID and gets no password.
	m := BuildManifest(sampleConfig(), map[string]string{"usb": "uuid-usb"}, "install-oldbox", time.Now())
	res, err := Adopt(&m, AdoptDecisions{ContinueAsMachine: true})
	if err != nil {
		t.Fatalf("Adopt: %v", err)
	}
	if len(res.MissingUUIDs) != 1 || res.MissingUUIDs[0] != "nas" {
		t.Errorf("MissingUUIDs = %v, want [nas]", res.MissingUUIDs)
	}
	if len(res.SharesNeedingPassword) != 1 || res.SharesNeedingPassword[0] != "nas" {
		t.Errorf("SharesNeedingPassword = %v, want [nas]", res.SharesNeedingPassword)
	}
}
