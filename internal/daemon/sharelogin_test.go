// SPDX-License-Identifier: MIT

package daemon

import (
	"bytes"
	"log/slog"
	"testing"

	"github.com/phil9922/backup-maker/internal/config"
)

func loginDaemon(t *testing.T, targets []config.Target, creds map[string]string) (*daemon, *config.Config) {
	t.Helper()
	cfg := &config.Config{
		General: config.General{MachineName: "my-laptop"},
		Targets: targets,
	}
	state := &config.State{IPCToken: "t", InstallID: "i", ShareCredentials: creds}
	return &daemon{
		log:   slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)),
		state: state,
		cfg:   cfg,
		marks: newSyncMarks(state),
	}, cfg
}

// THE GUARANTEE: browsing a destination this computer already backs up to does
// not ask for its password again.
//
// The credential was stored the day the destination was set up and is used by
// the mirror engine every few minutes. Nothing looked it up here, so the
// browse request arrived with empty credentials and the share refused it —
// which the UI showed as a password prompt. Adding a second folder to a
// destination in daily use meant retyping a password the daemon was already
// using, every time.
func TestBrowsingAConfiguredShareUsesTheStoredPassword(t *testing.T) {
	d, cfg := loginDaemon(t,
		[]config.Target{{Type: "share", Name: "backup-pi", URL: "//192.168.5.141/backups", Username: "pk"}},
		map[string]string{"backup-pi": "stored-secret"},
	)

	user, pass := d.storedShareLogin(cfg, "192.168.5.141")
	if user != "pk" || pass != "stored-secret" {
		t.Errorf("storedShareLogin = %q/%q, want the credentials already on file", user, pass)
	}
}

// A guest share is configured with no password, and that is a real answer
// rather than a missing one — the username still has to come back, or the
// caller falls through to prompting for credentials that do not exist.
func TestAGuestShareReturnsItsUsernameWithNoPassword(t *testing.T) {
	d, cfg := loginDaemon(t,
		[]config.Target{{Type: "share", Name: "open-nas", URL: "//nas.local/public"}},
		nil,
	)

	user, pass := d.storedShareLogin(cfg, "nas.local")
	if user != "" || pass != "" {
		t.Errorf("storedShareLogin = %q/%q, want empty for a guest share", user, pass)
	}
}

// A host this computer has no destination on must not be handed somebody
// else's credentials.
func TestAnUnknownHostGetsNoCredentials(t *testing.T) {
	d, cfg := loginDaemon(t,
		[]config.Target{{Type: "share", Name: "backup-pi", URL: "//192.168.5.141/backups", Username: "pk"}},
		map[string]string{"backup-pi": "stored-secret"},
	)

	if user, pass := d.storedShareLogin(cfg, "192.168.5.99"); user != "" || pass != "" {
		t.Errorf("a stranger was given %q/%q", user, pass)
	}
	if user, pass := d.storedShareLogin(cfg, "this"); user != "" || pass != "" {
		t.Errorf("the local machine was given share credentials: %q/%q", user, pass)
	}
	if user, pass := d.storedShareLogin(cfg, ""); user != "" || pass != "" {
		t.Errorf("an empty machine id was given %q/%q", user, pass)
	}
}

// A drive target has no credentials to lend, and must not be matched by host.
func TestADriveTargetIsNeverUsedAsAShareLogin(t *testing.T) {
	d, cfg := loginDaemon(t,
		[]config.Target{{Type: "drive", Name: "laptopcard", Path: "/media/pk/BACKUPCARD"}},
		map[string]string{"laptopcard": "not-a-share-password"},
	)

	if user, pass := d.storedShareLogin(cfg, "laptopcard"); user != "" || pass != "" {
		t.Errorf("a drive target lent credentials: %q/%q", user, pass)
	}
}
