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
