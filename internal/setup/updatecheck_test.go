// SPDX-License-Identifier: MIT

package setup

import (
	"testing"

	"github.com/phil9922/backup-maker/internal/config"
)

// OFF BY DEFAULT, AND THIS IS A PROMISE RATHER THAN A PREFERENCE. The README,
// docs/security.md and the dashboard all state that nothing reaches the internet
// unless the user asked for it. Update checking is the only part of the program
// that would contact an outside service on its own, so a fresh install must have
// it off — and this test is what stops that becoming untrue by accident.
func TestAFreshInstallChecksNothingOnTheInternet(t *testing.T) {
	cfg := config.New()
	if cfg.General.UpdateCheck {
		t.Error("update checking is on by default — a fresh install would contact github.com unasked")
	}
}

// It is independent of everything else, like every other delivery setting:
// switching it on must not disturb alerting, and switching alerting off must
// not silently start or stop it.
func TestUpdateCheckingIsIndependentOfTheOtherSettings(t *testing.T) {
	cfg := config.New()
	cfg.General.DesktopAlerts = true

	ApplySettingsTo(cfg, Settings{UpdateCheck: yes()})
	if !cfg.General.UpdateCheck {
		t.Fatal("update checking did not switch on")
	}
	if !cfg.General.DesktopAlerts {
		t.Error("switching update checking on switched desktop alerts off")
	}

	ApplySettingsTo(cfg, Settings{DesktopAlerts: no()})
	if !cfg.General.UpdateCheck {
		t.Error("switching desktop alerts off took update checking with it")
	}

	ApplySettingsTo(cfg, Settings{UpdateCheck: no()})
	if cfg.General.UpdateCheck {
		t.Error("update checking could not be switched back off")
	}
}

// And it survives the file the daemon actually reads, not just a struct.
func TestUpdateCheckSurvivesSavingAndLoading(t *testing.T) {
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
	if _, err := ApplySettings(Settings{UpdateCheck: yes()}); err != nil {
		t.Fatal(err)
	}
	reloaded, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	if !reloaded.General.UpdateCheck {
		t.Error("update_check = true did not survive the file")
	}
}
