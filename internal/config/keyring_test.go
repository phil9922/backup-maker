// SPDX-License-Identifier: MIT

package config

import (
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/zalando/go-keyring"
)

// These tests are in the order the risk is in. Keyring storage is opt-in, so the
// interesting cases are not "does it work" but "what happens on the machine where
// the keyring does not" — a headless Pi, a daemon started at boot with no keyring
// session, a locked keyring on a desktop. A backup tool is allowed to be less
// convenient there. It is not allowed to be broken, and it is never allowed to be
// broken quietly.
//
// No test here touches the real keyring: go-keyring's MockInit swaps the provider
// for an in-memory store, and MockInitWithError for one that refuses everything
// the way a locked keyring does.

// mockKeyring installs a fresh in-memory keyring and forgets what any earlier
// test wrote, so a Put in this test really writes. See ForgetKeyringCache.
func mockKeyring(t *testing.T) {
	t.Helper()
	keyring.MockInit()
	ForgetKeyringCache()
	t.Cleanup(ForgetKeyringCache)
}

// brokenKeyring installs a keyring that fails every operation, which is what a
// locked one, an absent secret-service provider and a missing D-Bus session all
// look like from here.
func brokenKeyring(t *testing.T, err error) {
	t.Helper()
	keyring.MockInitWithError(err)
	ForgetKeyringCache()
	t.Cleanup(ForgetKeyringCache)
}

// countKeyringWrites counts calls that actually reach the keyring, which is the
// only way to observe a call that is correctly NOT made.
func countKeyringWrites(t *testing.T) *int {
	t.Helper()
	var writes int
	real := keyringSet
	keyringSet = func(service, account, secret string) error {
		writes++
		return real(service, account, secret)
	}
	t.Cleanup(func() { keyringSet = real })
	return &writes
}

func TestALockedKeyringNeverLetsTheSentinelBeUsedAsAPasswordAndLosesNothing(t *testing.T) {
	dir := pointConfigDirInto(t, t.TempDir())
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	// The file as it looks on a machine where the passwords were moved into the
	// keyring: the names are still here, the values are placeholders.
	writeStateFile(t, `{
	  "ipc_token": "tok",
	  "secrets_in_keyring": true,
	  "share_credentials": {"pi-drive1": "`+KeyringSentinel+`"},
	  "archive_passwords": {"nightly": "`+KeyringSentinel+`"}
	}`)
	brokenKeyring(t, errors.New("no such interface on the session bus"))

	state, err := LoadState()
	if err != nil {
		t.Fatalf("a locked keyring made the state file unreadable, which stops the CLI and the daemon dead: %v", err)
	}

	// THE ONE THAT MATTERS. A sentinel left in the map is a string a caller will
	// use: the SMB client would log in with "<in-os-keyring>" and report a wrong
	// password on a machine where the password is right, and the snapshot writer
	// would encrypt a zip with it and lose that archive for good.
	if got, present := state.ShareCredentials["pi-drive1"]; present {
		t.Errorf("a share password that could not be read is present as %q; it must be absent so the caller misses rather than logging in with a placeholder", got)
	}
	if got, present := state.ArchivePasswords["nightly"]; present {
		t.Errorf("an archive password that could not be read is present as %q; a zip would be encrypted with it", got)
	}
	// And it says which ones, so the daemon can name them instead of leaving the
	// user with an unexplained authentication failure.
	if !state.SecretMissing(ShareKeyringAccount("pi-drive1")) {
		t.Error("the unreachable share password is not recorded as missing, so nothing can explain the failure")
	}
	if got := strings.Join(state.KeyringMisses(), ","); got != "archive/nightly,share/pi-drive1" {
		t.Errorf("KeyringMisses() = %q, want both entries in a stable order", got)
	}

	// A save while the keyring is still shut must not quietly forget that these
	// passwords exist: the file is the only record of WHICH secrets are in the
	// keyring, because a keyring cannot be asked to list them.
	state.BytesCopiedTotal = 1 // the ordinary reason a save happens at all
	if err := state.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}
	onDisk := readStateFile(t)
	for _, want := range []string{
		`"secrets_in_keyring": true`,
		`"pi-drive1": "` + KeyringSentinel + `"`,
		`"nightly": "` + KeyringSentinel + `"`,
	} {
		if !strings.Contains(onDisk, want) {
			t.Errorf("state.json no longer contains %s after a save with the keyring shut — the record of which passwords exist has been lost:\n%s", want, onDisk)
		}
	}
}

func TestKeyringStorageKeepsNoPlaintextPasswordInTheStateFileAndRoundTrips(t *testing.T) {
	dir := pointConfigDirInto(t, t.TempDir())
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	mockKeyring(t)

	state := &State{
		IPCToken:         "tok",
		SecretsInKeyring: true,
		ShareCredentials: map[string]string{"pi-drive1": "sharepw-not-in-the-file"},
		ArchivePasswords: map[string]string{"nightly": "zippw-not-in-the-file"},
	}
	if err := state.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}

	// Read as raw bytes, not as a State: the question is what is ON DISK.
	onDisk := readStateFile(t)
	for _, secret := range []string{"sharepw-not-in-the-file", "zippw-not-in-the-file"} {
		if strings.Contains(onDisk, secret) {
			t.Errorf("state.json still contains the plaintext password %q, which is the whole point of the feature:\n%s", secret, onDisk)
		}
	}
	// The KEYS stay, because nothing can ask a keyring what it holds.
	for _, key := range []string{"pi-drive1", "nightly"} {
		if !strings.Contains(onDisk, key) {
			t.Errorf("state.json does not name %q, so its password is in the keyring with nothing pointing at it", key)
		}
	}

	// The in-memory State is untouched by saving it — the daemon holds this exact
	// value and hands the share passwords to its mirror engines out of it.
	if state.ShareCredentials["pi-drive1"] != "sharepw-not-in-the-file" {
		t.Fatalf("Save redacted the live in-memory map (%q); every share destination would start failing to log in on the next flush", state.ShareCredentials["pi-drive1"])
	}

	back, err := LoadState()
	if err != nil {
		t.Fatalf("LoadState: %v", err)
	}
	if back.ShareCredentials["pi-drive1"] != "sharepw-not-in-the-file" {
		t.Errorf("share password came back as %q", back.ShareCredentials["pi-drive1"])
	}
	if back.ArchivePasswords["nightly"] != "zippw-not-in-the-file" {
		t.Errorf("archive password came back as %q", back.ArchivePasswords["nightly"])
	}
	if len(back.KeyringMisses()) != 0 {
		t.Errorf("a working keyring reported misses: %v", back.KeyringMisses())
	}
}

func TestAKeyringThatRefusesAWriteLeavesThePasswordInTheFileRatherThanLosingIt(t *testing.T) {
	dir := pointConfigDirInto(t, t.TempDir())
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	refusal := errors.New("the keyring collection is locked")
	brokenKeyring(t, refusal)

	var reported []string
	prev := KeyringWriteFallback
	KeyringWriteFallback = func(account string, err error) {
		if !errors.Is(err, refusal) {
			t.Errorf("fallback reported the wrong error for %s: %v", account, err)
		}
		reported = append(reported, account)
	}
	t.Cleanup(func() { KeyringWriteFallback = prev })

	state := &State{
		IPCToken:         "tok",
		SecretsInKeyring: true,
		ShareCredentials: map[string]string{"pi-drive1": "typed-by-hand-just-now"},
	}
	if err := state.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}

	// PLAINTEXT ON PURPOSE. The user asked for the password to be kept somewhere
	// better; they did not ask for it to be thrown away. state.json is mode 0600
	// and is where it lived before this feature existed.
	onDisk := readStateFile(t)
	if !strings.Contains(onDisk, "typed-by-hand-just-now") {
		t.Errorf("the keyring refused the write and the password is in neither storage — it is simply gone:\n%s", onDisk)
	}
	if strings.Contains(onDisk, KeyringSentinel) {
		t.Errorf("state.json points at a keyring entry that was never written:\n%s", onDisk)
	}
	// And the user is told, because a fallback nobody hears about is a lie.
	if len(reported) != 1 || reported[0] != ShareKeyringAccount("pi-drive1") {
		t.Errorf("the fallback to plaintext was reported as %v, want one entry for share/pi-drive1", reported)
	}

	// It also self-heals: the next save with a working keyring puts it away
	// properly, so a machine that was locked at the wrong moment does not stay in
	// plaintext for ever.
	mockKeyring(t)
	if err := state.Save(); err != nil {
		t.Fatalf("second Save: %v", err)
	}
	if healed := readStateFile(t); strings.Contains(healed, "typed-by-hand-just-now") {
		t.Errorf("a save with a working keyring left the password in the file:\n%s", healed)
	}
}

func TestAnIdleSaveDoesNotTouchTheKeyringAtAll(t *testing.T) {
	dir := pointConfigDirInto(t, t.TempDir())
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	mockKeyring(t)
	writes := countKeyringWrites(t)

	// The daemon saves on every tally flush — a few seconds apart on a busy
	// folder, for ever, while holding its own mutex. If an unchanged password cost
	// a keyring call, switching this feature on would turn an idle machine into a
	// permanent D-Bus conversation, and every one of those calls is a chance to
	// block or to raise an unlock prompt on somebody's desktop.
	state := &State{
		IPCToken:         "tok",
		SecretsInKeyring: true,
		ShareCredentials: map[string]string{"pi-drive1": "sharepw"},
		ArchivePasswords: map[string]string{"nightly": "zippw"},
	}
	if err := state.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if *writes != 2 {
		t.Fatalf("the first save made %d keyring writes, want one per password", *writes)
	}

	before := *writes
	for range 5 {
		state.BytesCopiedTotal++ // a flush of counters, which is what these are
		if err := state.Save(); err != nil {
			t.Fatalf("Save: %v", err)
		}
	}
	if *writes != before {
		t.Errorf("five idle saves made %d keyring writes; an unchanged password must cost none", *writes-before)
	}

	// A password that really changed still gets written, or the cache would be
	// hiding an update rather than skipping a no-op.
	state.ShareCredentials["pi-drive1"] = "changed"
	if err := state.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if *writes != before+1 {
		t.Errorf("a changed password made %d writes, want exactly 1", *writes-before)
	}
	if got, err := KeyringFetch(ShareKeyringAccount("pi-drive1")); err != nil || got != "changed" {
		t.Errorf("the keyring holds %q (err %v), want the changed password", got, err)
	}
}

func TestReadingAStateFileDoesNotTouchTheKeyringWhenTheFeatureIsOff(t *testing.T) {
	// The promise that makes this safe to ship: an install that never enables it
	// behaves exactly as it did before, including making no keyring calls at all —
	// which is what keeps a headless machine free of D-Bus errors it cannot fix.
	dir := pointConfigDirInto(t, t.TempDir())
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	brokenKeyring(t, errors.New("this keyring must never be asked"))
	writes := countKeyringWrites(t)

	state := &State{IPCToken: "tok", ShareCredentials: map[string]string{"pi-drive1": "sharepw"}}
	if err := state.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}
	back, err := LoadState()
	if err != nil {
		t.Fatalf("LoadState: %v", err)
	}
	if back.ShareCredentials["pi-drive1"] != "sharepw" {
		t.Errorf("password came back as %q", back.ShareCredentials["pi-drive1"])
	}
	if *writes != 0 {
		t.Errorf("%d keyring writes were made with the feature switched off", *writes)
	}
}

func TestAKeyringThatStopsAnsweringDoesNotStripPasswordsOutOfARunningDaemon(t *testing.T) {
	// The daemon re-reads state.json every time config.toml changes. If a keyring
	// relock between then and now took the share passwords away from a daemon that
	// already had them in memory and had been using them all day, saving an
	// unrelated setting would stop every share destination on the machine. That is
	// the failure keyring storage is not allowed to introduce.
	held := &State{
		SecretsInKeyring: true,
		ShareCredentials: map[string]string{"pi-drive1": "sharepw"},
		ArchivePasswords: map[string]string{"nightly": "zippw"},
	}
	reloaded := &State{
		SecretsInKeyring: true,
		MissingFromKeyring: map[string]bool{
			ShareKeyringAccount("pi-drive1"):   true,
			ArchiveKeyringAccount("nightly"):   true,
			ShareKeyringAccount("removed-nas"): true,
		},
	}
	reloaded.KeepReachableSecrets(held)

	if reloaded.ShareCredentials["pi-drive1"] != "sharepw" {
		t.Errorf("the share password was not kept from memory: %q", reloaded.ShareCredentials["pi-drive1"])
	}
	if reloaded.ArchivePasswords["nightly"] != "zippw" {
		t.Errorf("the archive password was not kept from memory: %q", reloaded.ArchivePasswords["nightly"])
	}
	if reloaded.SecretMissing(ShareKeyringAccount("pi-drive1")) {
		t.Error("a password recovered from memory is still reported as missing, which would refuse a rename that is perfectly safe")
	}
	// A destination whose password this process never held stays missing: the
	// keyring is the only place it exists, and pretending otherwise would hide the
	// failure the warning at startup exists to report.
	if !reloaded.SecretMissing(ShareKeyringAccount("removed-nas")) {
		t.Error("an account nothing in memory could supply was cleared anyway")
	}
}

func writeStateFile(t *testing.T, contents string) {
	t.Helper()
	path, err := StatePath()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
}

func readStateFile(t *testing.T) string {
	t.Helper()
	path, err := StatePath()
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}
