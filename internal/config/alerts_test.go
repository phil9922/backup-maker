// SPDX-License-Identifier: MIT

package config

import (
	"testing"

	toml "github.com/pelletier/go-toml/v2"
)

// Desktop alerts default to ON, and — the part that is easy to get wrong — an
// explicit "off" has to survive being written back to the file. With omitempty
// on that field, `desktop_alerts = false` would be dropped on the next save and
// silently read as true again, which is a setting that ignores the user.
func TestDesktopAlertsDefaultOnAndAnExplicitOffSurvivesSaving(t *testing.T) {
	if !New().General.DesktopAlerts {
		t.Error("desktop alerts are off by default; silent failure is what they exist to prevent")
	}

	// A config file written before this setting existed has no such key.
	old := New()
	if err := toml.Unmarshal([]byte("[general]\nmachine_name = \"laptop\"\n"), old); err != nil {
		t.Fatal(err)
	}
	if !old.General.DesktopAlerts {
		t.Error("an existing config without the key came out with alerts off")
	}

	off := New()
	if err := toml.Unmarshal([]byte("[general]\ndesktop_alerts = false\n"), off); err != nil {
		t.Fatal(err)
	}
	if off.General.DesktopAlerts {
		t.Fatal("desktop_alerts = false was ignored")
	}

	data, err := toml.Marshal(off)
	if err != nil {
		t.Fatal(err)
	}
	reloaded := New()
	if err := toml.Unmarshal(data, reloaded); err != nil {
		t.Fatal(err)
	}
	if reloaded.General.DesktopAlerts {
		t.Errorf("saving turned the alerts back on:\n%s", data)
	}
}

// THE UPGRADE TRAP, and the reason every field of AlertKinds is a pointer.
// Not one config file in existence has an [general.alerts] table. If these were
// plain bools they would all parse as false — and every user of an upgraded
// backup-maker would stop being told their backups had stopped, having changed
// nothing and been asked nothing.
func TestAConfigWrittenBeforePerAlertSettingsExistedKeepsEveryAlertOn(t *testing.T) {
	old := New()
	if err := toml.Unmarshal([]byte("[general]\nmachine_name = \"laptop\"\ndesktop_alerts = true\n"), old); err != nil {
		t.Fatal(err)
	}
	a := old.General.Alerts
	for name, got := range map[string]bool{
		"backups stopped":      a.BackupsStoppedOn(),
		"snapshot failed":      a.SnapshotFailedOn(),
		"unrecognised storage": a.UnrecognisedStorageOn(),
		"pair requests":        a.PairRequestsOn(),
	} {
		if !got {
			t.Errorf("upgrading silently switched off the %q alert", name)
		}
	}
}

// And the other half: switching one off has to stick. A per-category setting
// that quietly turns itself back on is worse than not offering the choice.
func TestSwitchingOneAlertCategoryOffSurvivesSaving(t *testing.T) {
	c := New()
	if err := toml.Unmarshal([]byte("[general.alerts]\nsnapshot_failed = false\n"), c); err != nil {
		t.Fatal(err)
	}
	if c.General.Alerts.SnapshotFailedOn() {
		t.Fatal("snapshot_failed = false was ignored")
	}
	data, err := toml.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	reloaded := New()
	if err := toml.Unmarshal(data, reloaded); err != nil {
		t.Fatal(err)
	}
	if reloaded.General.Alerts.SnapshotFailedOn() {
		t.Errorf("saving turned the snapshot alert back on:\n%s", data)
	}
	// The categories the user did not touch must still be on — switching one
	// off must not be a way of switching the rest off by omission.
	if !reloaded.General.Alerts.BackupsStoppedOn() || !reloaded.General.Alerts.PairRequestsOn() {
		t.Errorf("switching one category off disabled the others:\n%s", data)
	}
}
