// SPDX-License-Identifier: MIT

package setup

import (
	"os"
	"path/filepath"
	"strings"
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

// sampleManifestFor is the manifest as it would sit on one named destination:
// that one described in full, the rest by name and type. Tests say which
// destination they are adopting from, because that is now what decides what
// they get back.
func sampleManifestFor(writtenFor string) *Manifest {
	m := BuildManifest(sampleConfig(), map[string]string{"usb": "uuid-usb", "nas": "uuid-nas"}, "install-oldbox", time.Now(), writtenFor)
	return &m
}

// sampleManifest is the drive's copy: usb in full, nas summarised.
func sampleManifest() *Manifest { return sampleManifestFor("usb") }

// fullManifest is what every destination carried before manifests were scoped,
// and what the drives already in service still hold. Built by hand because
// BuildManifest can no longer produce one.
func fullManifest() *Manifest {
	cfg := sampleConfig()
	uuids := map[string]string{"usb": "uuid-usb", "nas": "uuid-nas"}
	m := BuildManifest(cfg, uuids, "install-oldbox", time.Now(), "")
	for i, t := range cfg.Targets {
		m.Targets[i] = ManifestTarget{Target: t, UUID: uuids[t.Name]}
	}
	return &m
}

func TestManifestRoundTrip(t *testing.T) {
	b := localmirror.NewLocalFS(t.TempDir())
	uuids := map[string]string{"usb": "uuid-usb", "nas": "uuid-nas"}
	if err := WriteManifest(b, sampleConfig(), uuids, "install-oldbox", "usb"); err != nil {
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
	// The destination this manifest is on, in full.
	if m.Targets[0].UUID != "uuid-usb" || m.Targets[0].Path != "/mnt/usb" || !m.Targets[0].Locatable() {
		t.Errorf("the drive's own entry did not round-trip: %+v", m.Targets[0])
	}
	// The other one, by name and type alone. Its UUID is as private as its
	// address: it is the value that proves which physical drive that is.
	nas := m.Targets[1]
	if nas.Name != "nas" || nas.Type != "share" {
		t.Errorf("the other destination should still be named: %+v", nas)
	}
	if nas.Locatable() || nas.URL != "" || nas.Username != "" || nas.UUID != "" {
		t.Errorf("the other destination leaked how to reach it: %+v", nas)
	}
}

// The guarantee this scoping exists for, asserted on the BYTES that land on the
// drive rather than on the struct — a field could be dropped from Locatable's
// reckoning and still be serialised.
func TestAManifestNeverCarriesAnotherDestinationsAddress(t *testing.T) {
	dir := t.TempDir()
	cfg := sampleConfig()
	cfg.Targets[1].MAC = "aa:bb:cc:dd:ee:ff"
	cfg.Targets = append(cfg.Targets, config.Target{
		Type: "device", Name: "workshop-pi", DeviceID: "AAAAAAA-BBBBBBB-CCCCCCC",
	})
	if err := WriteManifest(localmirror.NewLocalFS(dir), cfg,
		map[string]string{"usb": "uuid-usb", "nas": "uuid-nas"}, "install-oldbox", "usb"); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, config.MachineDir("oldbox"), ManifestName))
	if err != nil {
		t.Fatal(err)
	}
	got := string(raw)
	for _, secret := range []string{
		"//nas/backups",           // another destination's address
		"phil",                    // its SMB username
		"aa:bb:cc:dd:ee:ff",       // its MAC
		"AAAAAAA-BBBBBBB-CCCCCCC", // a paired machine's device id
		"uuid-nas",                // which physical drive it is
	} {
		if strings.Contains(got, secret) {
			t.Errorf("a drive that can be lost carries %q:\n%s", secret, got)
		}
	}
	// It must still say they EXIST, or adoption cannot tell anybody what is
	// missing and the summaries are pure loss. The key is capitalised because
	// config.Target carries toml tags and no json ones, so encoding/json falls
	// back to the Go field names.
	for _, name := range []string{"nas", "workshop-pi"} {
		if !strings.Contains(got, `"Name": "`+name+`"`) {
			t.Errorf("the manifest should still name %q so adoption can report it:\n%s", name, got)
		}
	}
	// And this destination's own details must survive, or nothing can be
	// adopted from it at all.
	for _, own := range []string{"/mnt/usb", "uuid-usb"} {
		if !strings.Contains(got, own) {
			t.Errorf("the manifest lost its OWN %q:\n%s", own, got)
		}
	}
}

// A caller that does not say which destination it is writing for gets the
// private answer, not the convenient one. Asserted because the failure is
// silent in the direction that matters: the manifest still works, and every
// address is on the drive.
func TestAManifestForNoNamedDestinationDescribesNone(t *testing.T) {
	m := BuildManifest(sampleConfig(), map[string]string{"usb": "uuid-usb", "nas": "uuid-nas"},
		"install-oldbox", time.Now(), "")
	if len(m.Targets) != 2 {
		t.Fatalf("targets = %d, want both still named", len(m.Targets))
	}
	for _, mt := range m.Targets {
		if mt.Locatable() || mt.UUID != "" {
			t.Errorf("an unscoped manifest described %q: %+v", mt.Name, mt)
		}
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
	if st.DriveTargetUUIDs["usb"] != "uuid-usb" {
		t.Errorf("uuid not recorded for the drive adopted from: %+v", st.DriveTargetUUIDs)
	}
	if st.ArchivePasswords["weekly"] != "zippw" {
		t.Errorf("archive password not stored: %q", st.ArchivePasswords["weekly"])
	}
	if !st.SetupComplete {
		t.Error("SetupComplete should be true after adopt")
	}

	// The share was only NAMED by this drive's manifest, so it is reported for
	// re-adding rather than restored — and no credential is stored for a target
	// that does not exist.
	if len(res.NeedReadding) != 1 || res.NeedReadding[0].Name != "nas" || res.NeedReadding[0].Type != "share" {
		t.Errorf("NeedReadding = %+v, want the nas share", res.NeedReadding)
	}
	if _, ok := st.ShareCredentials["nas"]; ok {
		t.Errorf("a password was stored for a target that was not restored: %+v", st.ShareCredentials)
	}
	for _, tg := range cfg.Targets {
		if tg.Name == "nas" {
			t.Fatalf("a destination the manifest could not locate was restored anyway: %+v", tg)
		}
	}
}

// The other half of the round trip: adopting from the SHARE restores the share
// and reports the drive. Whichever destination somebody still has is the one
// that rebuilds.
func TestAdoptingFromTheShareRestoresTheShareAndReportsTheDrive(t *testing.T) {
	isolateNoConfig(t)
	res, err := Adopt(sampleManifestFor("nas"), AdoptDecisions{
		ContinueAsMachine: true,
		SharePasswords:    map[string]string{"nas": "sharepw"},
	})
	if err != nil {
		t.Fatalf("Adopt: %v", err)
	}
	cfg := load(t)
	if len(cfg.Targets) != 1 || cfg.Targets[0].Name != "nas" || cfg.Targets[0].URL != "//nas/backups" {
		t.Fatalf("targets = %+v, want just the nas share in full", cfg.Targets)
	}
	if cfg.Targets[0].Username != "phil" {
		t.Errorf("the adopted share lost its username: %+v", cfg.Targets[0])
	}
	if len(res.NeedReadding) != 1 || res.NeedReadding[0].Name != "usb" {
		t.Errorf("NeedReadding = %+v, want the usb drive", res.NeedReadding)
	}
	st, err := config.LoadState()
	if err != nil {
		t.Fatal(err)
	}
	if st.ShareCredentials["nas"] != "sharepw" {
		t.Errorf("share password not stored: %q", st.ShareCredentials["nas"])
	}
}

// A SNAPSHOT SCHEDULE WHOSE DESTINATION WAS NOT RESTORED MUST NOT FAIL THE
// WHOLE ADOPTION. config.Validate refuses an archive naming an unknown target,
// so carrying it would turn "one schedule to re-create" into "this drive cannot
// be adopted at all" — on the flow somebody reaches on their worst day.
func TestAScheduleForAnUnrestoredDestinationIsReportedNotFatal(t *testing.T) {
	isolateNoConfig(t)
	// The weekly archive writes to usb; adopt from the share, so usb is only
	// named.
	res, err := Adopt(sampleManifestFor("nas"), AdoptDecisions{ContinueAsMachine: true})
	if err != nil {
		t.Fatalf("adoption failed over a schedule it could have reported: %v", err)
	}
	if len(res.SkippedArchives) != 1 || res.SkippedArchives[0] != "weekly" {
		t.Errorf("SkippedArchives = %v, want [weekly]", res.SkippedArchives)
	}
	cfg := load(t)
	if len(cfg.Archives) != 0 {
		t.Errorf("a schedule pointing at nothing was kept: %+v", cfg.Archives)
	}
	if err := cfg.Validate(); err != nil {
		t.Errorf("the adopted config does not validate: %v", err)
	}
}

// A summarised entry carries no Folders list, and an EMPTY folders list means
// EVERY folder. Restoring one would hand a destination every folder on the
// machine — the widening this project has already been bitten by twice. The
// defence is that it is never restored at all; this is the test named after it.
func TestASummarisedDestinationIsNeverRestoredWithEveryFolder(t *testing.T) {
	isolateNoConfig(t)
	if _, err := Adopt(sampleManifestFor("usb"), AdoptDecisions{ContinueAsMachine: true}); err != nil {
		t.Fatalf("Adopt: %v", err)
	}
	for _, tg := range load(t).Targets {
		if tg.Name == "nas" {
			t.Fatalf("nas was restored from a summary, scoped to %v folders", tg.Folders)
		}
	}
}

// THE DRIVES ALREADY IN SERVICE HOLD FULL MANIFESTS. Scoping must not make one
// of them restore less than it did the day it was written, or the change costs
// exactly the people it was meant to protect.
func TestAFullManifestWrittenBeforeScopingStillRestoresEverything(t *testing.T) {
	isolateNoConfig(t)
	res, err := Adopt(fullManifest(), AdoptDecisions{
		ContinueAsMachine: true,
		SharePasswords:    map[string]string{"nas": "sharepw"},
		ArchivePasswords:  map[string]string{"weekly": "zippw"},
	})
	if err != nil {
		t.Fatalf("Adopt: %v", err)
	}
	if len(res.NeedReadding) != 0 || len(res.SkippedArchives) != 0 {
		t.Errorf("an older full manifest lost something: NeedReadding=%+v Skipped=%v",
			res.NeedReadding, res.SkippedArchives)
	}
	cfg := load(t)
	if len(cfg.Targets) != 2 || len(cfg.Archives) != 1 {
		t.Fatalf("restored %d targets and %d archives, want 2 and 1", len(cfg.Targets), len(cfg.Archives))
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
	// The share's own manifest, so it is described in full — but no UUID was
	// recorded for it and no password is offered. A target reported this way is
	// one adoption DID restore and cannot yet use, which is a different thing
	// from one it never had the address for (NeedReadding).
	m := BuildManifest(sampleConfig(), map[string]string{"usb": "uuid-usb"}, "install-oldbox", time.Now(), "nas")
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
