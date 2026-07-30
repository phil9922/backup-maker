// SPDX-License-Identifier: MIT

package cmd

import (
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/zalando/go-keyring"

	"github.com/phil9922/backup-maker/internal/config"
)

// isolateForKeychain points the config directory at a throwaway location, swaps
// the OS keyring for an in-memory one, and swallows the commands' output.
//
// The real keyring is never touched by any test here: writing a developer's
// actual login keyring from `go test` would be a side effect nobody consented to,
// and reading one would make the result depend on whose machine it ran on.
func isolateForKeychain(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir) // linux
	t.Setenv("AppData", dir)         // windows
	t.Setenv("HOME", dir)            // macOS
	keyring.MockInit()
	config.ForgetKeyringCache()
	t.Cleanup(config.ForgetKeyringCache)

	devnull, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	stdout := os.Stdout
	os.Stdout = devnull
	t.Cleanup(func() {
		os.Stdout = stdout
		devnull.Close()
	})
}

func stateFileText(t *testing.T) string {
	t.Helper()
	path, err := config.StatePath()
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// THE GUARANTEE: switching keyring storage on moves the passwords and switching
// it off puts them back, with nothing lost in either direction.
//
// The round trip is one test rather than two because "you can change your mind"
// is the promise, and a migration that only goes one way is a trap: somebody
// enables this on a machine, finds their boot-started daemon cannot read the
// keyring, and needs the way back to be certain rather than a repair job.
func TestEnablingKeyringStorageMovesThePasswordsAndDisablingPutsThemBack(t *testing.T) {
	isolateForKeychain(t)
	state := &config.State{
		IPCToken:         "tok",
		ShareCredentials: map[string]string{"pi-drive1": "sharepw"},
		ArchivePasswords: map[string]string{"nightly": "zippw"},
	}
	if err := state.Save(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stateFileText(t), "sharepw") {
		t.Fatal("the test did not start from passwords in the file")
	}

	if err := keychainEnableCmd.RunE(keychainEnableCmd, nil); err != nil {
		t.Fatalf("keychain enable: %v", err)
	}

	// The file holds placeholders and the names, and no passwords.
	enabled := stateFileText(t)
	for _, secret := range []string{"sharepw", "zippw"} {
		if strings.Contains(enabled, secret) {
			t.Errorf("state.json still holds the plaintext %q after enabling:\n%s", secret, enabled)
		}
	}
	for _, name := range []string{"pi-drive1", "nightly"} {
		if !strings.Contains(enabled, name) {
			t.Errorf("state.json no longer names %q, so nothing can find its password again", name)
		}
	}
	if !strings.Contains(enabled, `"secrets_in_keyring": true`) {
		t.Errorf("the flag was not recorded, so the next load would read placeholders as passwords:\n%s", enabled)
	}

	// The keyring holds the real values, under the documented account names.
	for account, want := range map[string]string{
		config.ShareKeyringAccount("pi-drive1"): "sharepw",
		config.ArchiveKeyringAccount("nightly"): "zippw",
	} {
		got, err := keyring.Get(config.KeyringService, account)
		if err != nil {
			t.Errorf("the keyring does not hold %s: %v", account, err)
			continue
		}
		if got != want {
			t.Errorf("the keyring holds %q for %s, want %q", got, account, want)
		}
	}

	// And a fresh load sees the real passwords, which is what keeps every caller
	// in the program unaware that any of this happened.
	loaded, err := config.LoadState()
	if err != nil {
		t.Fatal(err)
	}
	if loaded.ShareCredentials["pi-drive1"] != "sharepw" || loaded.ArchivePasswords["nightly"] != "zippw" {
		t.Fatalf("loading after enable did not restore the passwords: %+v %+v", loaded.ShareCredentials, loaded.ArchivePasswords)
	}

	if err := keychainDisableCmd.RunE(keychainDisableCmd, nil); err != nil {
		t.Fatalf("keychain disable: %v", err)
	}

	disabled := stateFileText(t)
	for _, secret := range []string{"sharepw", "zippw"} {
		if !strings.Contains(disabled, secret) {
			t.Errorf("disabling did not put %q back in state.json, so the password is gone:\n%s", secret, disabled)
		}
	}
	if strings.Contains(disabled, config.KeyringSentinel) {
		t.Errorf("state.json still points at keyring entries after disabling:\n%s", disabled)
	}
	// The copies in the keyring are gone: leaving them would mean a password the
	// user asked to have removed from the keyring is still in it.
	for _, account := range []string{config.ShareKeyringAccount("pi-drive1"), config.ArchiveKeyringAccount("nightly")} {
		if _, err := keyring.Get(config.KeyringService, account); !errors.Is(err, keyring.ErrNotFound) {
			t.Errorf("keyring entry %s survived disabling (err %v)", account, err)
		}
	}
}

// THE GUARANTEE: on a machine with no usable keyring, `enable` refuses and
// changes nothing.
//
// This is the headless Raspberry Pi, and the daemon started at boot with no
// keyring session. It is the first case worth testing rather than an edge one:
// a migration that half-completed there would leave the passwords in a keyring
// that cannot be read, on the machine least able to be sat down in front of.
func TestEnablingRefusesOnAMachineWithNoUsableKeyringAndChangesNothing(t *testing.T) {
	isolateForKeychain(t)
	keyring.MockInitWithError(errors.New("dbus: couldn't determine address of session bus"))

	state := &config.State{IPCToken: "tok", ShareCredentials: map[string]string{"pi-drive1": "sharepw"}}
	if err := state.Save(); err != nil {
		t.Fatal(err)
	}
	before := stateFileText(t)

	err := keychainEnableCmd.RunE(keychainEnableCmd, nil)
	if err == nil {
		t.Fatal("enable succeeded with no keyring: the passwords would be unreachable on the next start")
	}
	// The refusal has to name the situation, not just fail: "no keyring session"
	// is a fact about how the daemon was started, and somebody has to be able to
	// act on it.
	if !strings.Contains(err.Error(), "state.json") {
		t.Errorf("the refusal does not say where the passwords stayed: %v", err)
	}
	if after := stateFileText(t); after != before {
		t.Errorf("state.json was modified by a refused enable:\nbefore %s\nafter %s", before, after)
	}
}

// THE GUARANTEE: `disable` refuses while any password cannot be read out of the
// keyring, because switching off means writing them back and one that cannot be
// read cannot be written.
func TestDisablingRefusesWhileAPasswordCannotBeReadBack(t *testing.T) {
	isolateForKeychain(t)
	// The file as a machine looks after enabling, with the keyring now shut.
	path, err := config.StatePath()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`{
	  "ipc_token": "tok",
	  "secrets_in_keyring": true,
	  "share_credentials": {"pi-drive1": "`+config.KeyringSentinel+`"}
	}`), 0o600); err != nil {
		t.Fatal(err)
	}
	before := stateFileText(t)
	keyring.MockInitWithError(errors.New("the keyring collection is locked"))

	err = keychainDisableCmd.RunE(keychainDisableCmd, nil)
	if err == nil {
		t.Fatal("disable succeeded with the keyring shut: it would have written a file with the password simply absent, then deleted the keyring copy")
	}
	if !strings.Contains(err.Error(), "share/pi-drive1") {
		t.Errorf("the refusal does not say which password it could not read: %v", err)
	}
	if after := stateFileText(t); after != before {
		t.Errorf("state.json was modified by a refused disable:\nbefore %s\nafter %s", before, after)
	}
}
