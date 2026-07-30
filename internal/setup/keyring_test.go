// SPDX-License-Identifier: MIT

package setup

import (
	"errors"
	"strings"
	"testing"

	"github.com/zalando/go-keyring"

	"github.com/phil9922/backup-maker/internal/config"
	"github.com/phil9922/backup-maker/internal/testpath"
)

// mockKeyring swaps the OS keyring for an in-memory one and forgets whatever an
// earlier test wrote, so a save in this test really reaches the store. No test
// in this package touches a real keyring.
func mockKeyring(t *testing.T) {
	t.Helper()
	keyring.MockInit()
	config.ForgetKeyringCache()
	t.Cleanup(config.ForgetKeyringCache)
}

// shareTargetConfig is the one-destination config these tests rename and remove.
//
// Folders is set EXPLICITLY on every target, empty and on purpose, because an
// empty list means every folder and there are no folders here — the point being
// that a Target must never be built without that decision being made out loud.
func shareTargetConfig(t *testing.T) {
	t.Helper()
	cfg := config.New()
	cfg.General.MachineName = "my-laptop"
	cfg.Targets = []config.Target{
		{Type: "share", Name: "backups", URL: "//pi/backups", Username: "alex", Folders: []string{}},
	}
	if err := cfg.Save(); err != nil {
		t.Fatal(err)
	}
}

// THE GUARANTEE: renaming a destination takes its keyring entry with it, so the
// password is still reachable under the new name.
//
// The failure this prevents is the quietest one in the feature. A rename is
// entirely a local re-keying, so it "works" while the OS keyring goes on holding
// the password under the old account name — and nothing notices until the share
// fails to log in weeks later and asks for a password the user was told they no
// longer had to remember.
func TestRenamingADestinationTakesItsKeyringEntryWithIt(t *testing.T) {
	isolate(t)
	mockKeyring(t)
	shareTargetConfig(t)

	state, err := config.LoadState()
	if err != nil {
		t.Fatal(err)
	}
	state.SecretsInKeyring = true
	state.DriveTargetUUIDs = map[string]string{"backups": "uuid-pi"}
	state.ShareCredentials = map[string]string{"backups": "the-password"}
	if err := state.Save(); err != nil {
		t.Fatal(err)
	}
	if got, err := keyring.Get(config.KeyringService, config.ShareKeyringAccount("backups")); err != nil || got != "the-password" {
		t.Fatalf("the test did not start from a password in the keyring: %q %v", got, err)
	}

	if err := RenameTarget("backups", "pi-drive1"); err != nil {
		t.Fatal(err)
	}

	if got, err := keyring.Get(config.KeyringService, config.ShareKeyringAccount("pi-drive1")); err != nil || got != "the-password" {
		t.Errorf("the keyring holds %q for the new name (err %v), want the password to have followed the rename", got, err)
	}
	if _, err := keyring.Get(config.KeyringService, config.ShareKeyringAccount("backups")); !errors.Is(err, keyring.ErrNotFound) {
		t.Errorf("a copy of the password is still filed under the old name (err %v); nothing points at it and it will outlive the destination", err)
	}
	// And the whole point of the move: loading finds it again.
	after, err := config.LoadState()
	if err != nil {
		t.Fatal(err)
	}
	if after.ShareCredentials["pi-drive1"] != "the-password" {
		t.Errorf("after the rename the password reads as %q; it would be asked for again", after.ShareCredentials["pi-drive1"])
	}
}

// THE GUARANTEE: a rename is REFUSED while the destination's password is locked
// in the OS keyring, rather than completed with the password left behind.
//
// With the keyring shut there is nothing in the map to move, so every line of
// RenameTarget would succeed and report success: config.toml would name the
// destination something new, and the password would still be filed in the keyring
// under the old name with nothing on the machine pointing at it. That is data
// loss on a flow that looks like it worked. Renaming is never urgent; unlocking a
// keyring is a moment's work.
func TestRenamingIsRefusedWhileThePasswordIsLockedInTheKeyring(t *testing.T) {
	isolate(t)
	shareTargetConfig(t)

	// The state file as it stands on a machine that has enabled keyring storage.
	mockKeyring(t)
	state, err := config.LoadState()
	if err != nil {
		t.Fatal(err)
	}
	state.SecretsInKeyring = true
	state.ShareCredentials = map[string]string{"backups": "the-password"}
	if err := state.Save(); err != nil {
		t.Fatal(err)
	}

	// ...and now the keyring will not answer: locked, or a daemon started at boot
	// with no keyring session.
	keyring.MockInitWithError(errors.New("the keyring collection is locked"))
	config.ForgetKeyringCache()

	err = RenameTarget("backups", "pi-drive1")
	if err == nil {
		t.Fatal("the rename went ahead with the password out of reach: it is now filed under a name nothing refers to")
	}
	// The refusal has to say what to do about it. A dead-end refusal on a rename
	// is what the naming work in this file exists to remove.
	for _, want := range []string{"keyring", "backups"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal does not mention %q, so the user cannot act on it: %v", want, err)
		}
	}
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Targets[0].Name != "backups" {
		t.Errorf("the destination was renamed to %q despite the refusal", cfg.Targets[0].Name)
	}
}

// THE GUARANTEE: removing a destination removes its keyring entry too, so the two
// storages forget the same thing.
//
// Nothing on the destination itself is touched — this is one machine forgetting a
// credential, which is the only kind of deletion the removal paths do at all.
func TestRemovingADestinationAlsoRemovesItsKeyringEntry(t *testing.T) {
	isolate(t)
	mockKeyring(t)
	cfg := config.New()
	cfg.General.MachineName = "my-laptop"
	cfg.Folders = []config.Folder{{ID: "fold1", Path: testpath.Abs("/home/alex/code"), Label: "code"}}
	// Folders set explicitly, empty and deliberately: see shareTargetConfig.
	cfg.Targets = []config.Target{
		{Type: "share", Name: "backups", URL: "//pi/backups", Username: "alex", Folders: []string{}},
	}
	cfg.Archives = []config.Archive{{Name: "nightly", Every: "daily", Target: "backups", Keep: 3}}
	if err := cfg.Save(); err != nil {
		t.Fatal(err)
	}
	state, err := config.LoadState()
	if err != nil {
		t.Fatal(err)
	}
	state.SecretsInKeyring = true
	state.ShareCredentials = map[string]string{"backups": "the-password"}
	state.ArchivePasswords = map[string]string{"nightly": "zippw"}
	if err := state.Save(); err != nil {
		t.Fatal(err)
	}

	if err := RemoveTarget("backups"); err != nil {
		t.Fatal(err)
	}

	// The share password, and the snapshot password of the schedule that was
	// orphaned by losing its destination.
	for _, account := range []string{
		config.ShareKeyringAccount("backups"),
		config.ArchiveKeyringAccount("nightly"),
	} {
		if _, err := keyring.Get(config.KeyringService, account); !errors.Is(err, keyring.ErrNotFound) {
			t.Errorf("keyring entry %s survived the removal (err %v); the file no longer names it, so nothing can ever remove it from inside backup-maker", account, err)
		}
	}
}
