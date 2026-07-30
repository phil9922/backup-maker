// SPDX-License-Identifier: MIT

package config

import (
	"errors"
	"fmt"
	"sync"

	"github.com/zalando/go-keyring"
)

// This file is the whole of what backup-maker knows about the OS keyring:
// GNOME Keyring / KWallet over D-Bus on Linux, the Keychain on macOS, the
// Credential Manager on Windows. It holds exactly two kinds of secret — the
// share passwords and the snapshot passwords — and it is OFF UNTIL SOMEBODY
// TURNS IT ON with `backup-maker keychain enable`.
//
// WHY IT IS OPT-IN AND WHY THAT IS NOT A COMPROMISE. state.json is mode 0600 in
// the user's own config directory: on a single-user machine that is the same
// trust boundary as the keyring's own on-disk store, and the keyring's advantage
// is real but narrower than it sounds — it is encryption at rest for a file that
// is only readable by the account whose login unlocks the keyring anyway. What
// the keyring costs, though, is availability, and this program's entire job is
// availability. A headless Raspberry Pi has no keyring daemon at all. A daemon
// started at boot rather than at login has no keyring SESSION even where the
// daemon exists, so `Get` fails on a machine where everything is installed
// correctly. A locked keyring behaves the same way. So the storage that cannot
// fail is the default, and the storage that can is a choice made by somebody who
// knows their machine — which is the reverse of the usual instinct, and correct
// here for the same reason the whole program is one-way: a backup tool that
// stops backing up has failed at the only thing it does.
//
// THE FILE STILL LISTS THE KEYS. go-keyring has no enumeration API on any
// platform (the Windows Credential Manager barely has one at all), so nothing
// can ask the keyring "which of these do you hold". state.json remains the
// record of WHICH secrets exist, with a sentinel where each value was. Lose that
// list and the secrets are unreachable even though they are still stored.

// KeyringService is the service name every entry of ours is filed under, and it
// is what the user sees in Seahorse or Keychain Access. One service for both
// kinds of secret, with the kind in the account name, so that a person auditing
// their keyring sees one recognisable group rather than two.
const KeyringService = "backup-maker"

// KeyringSentinel is written into state.json where a secret used to be, so the
// file says "this exists and is held elsewhere" rather than going quiet about it.
//
// A SENTENCE RATHER THAN A TOKEN, and with no angle brackets in it, because
// somebody will open this file to see what happened to their password.
// encoding/json escapes angle brackets and ampersands even inside a string, so
// the obvious `<in-os-keyring>` lands on disk with both brackets replaced by
// six-character unicode escapes — which turns the one line a person might
// actually read into something that looks like corruption.
//
// It does not have to be unforgeable: a user whose real SMB password is literally
// this string still round-trips correctly, because the sentinel is only consulted
// while the in-keyring flag is on and their value goes into the keyring and comes
// back out of it like any other. The single odd case is a keyring write that
// failed while the real secret happened to be this exact sentence, which would
// then be read back from the file as a placeholder and reported as missing rather
// than used — a wrong password reported honestly, not a silent one.
const KeyringSentinel = "(stored in the OS keyring)"

// The two kinds of secret, PREFIXED because the maps they come from are keyed by
// different things which can collide. ShareCredentials is keyed by destination
// name and ArchivePasswords by snapshot-schedule name; a destination called
// "photos" and a nightly zip called "photos" are unrelated, and one keyring
// account serving both would hand the SMB password to the zip writer and lock
// the archive with something nobody can reproduce.
const (
	shareAccountPrefix   = "share/"
	archiveAccountPrefix = "archive/"
)

// ShareKeyringAccount names the keyring entry holding one destination's SMB
// password.
func ShareKeyringAccount(target string) string { return shareAccountPrefix + target }

// ArchiveKeyringAccount names the keyring entry holding one snapshot schedule's
// zip password.
func ArchiveKeyringAccount(archive string) string { return archiveAccountPrefix + archive }

// The entire surface this program uses of go-keyring, held in variables rather
// than called directly for ONE reason: the cache below promises that an idle
// daemon makes no keyring calls at all, and a promise about a call that is *not*
// made cannot be tested by inspecting a store afterwards. go-keyring's mock
// records values, not calls. Never reassigned outside tests.
var (
	keyringSet    = keyring.Set
	keyringGet    = keyring.Get
	keyringDelete = keyring.Delete
)

// What this process last saw in the keyring for each account, so that writing a
// value the keyring already holds costs nothing.
//
// THIS IS NOT AN OPTIMISATION, IT IS THE FEATURE BEING SURVIVABLE. State.Save
// runs on every tally flush — a few seconds apart on a busy folder, for ever —
// and it runs while the daemon holds its own mutex. Without this, switching the
// keyring on would turn an idle machine into a permanent D-Bus conversation, and
// every one of those calls is a chance to block or to raise an unlock prompt on
// somebody's desktop. With it, an idle flush touches the keyring zero times, and
// the only calls made are the ones that follow an actual password change.
//
// Populated by reads as well as writes: a value just read back IS what the
// keyring holds, so the first Save after a daemon start is free too.
var (
	keyringCacheMu sync.Mutex
	keyringCache   = map[string]string{}
)

// KeyringPut stores one secret, and does nothing whatsoever when the keyring is
// already known to hold that exact value. See keyringCache for why the skip
// matters more than the write.
func KeyringPut(account, secret string) error {
	keyringCacheMu.Lock()
	cached, known := keyringCache[account]
	keyringCacheMu.Unlock()
	if known && cached == secret {
		return nil
	}
	if err := keyringSet(KeyringService, account, secret); err != nil {
		return err
	}
	keyringCacheMu.Lock()
	keyringCache[account] = secret
	keyringCacheMu.Unlock()
	return nil
}

// KeyringFetch reads one secret back out.
//
// Never caches a failure: a keyring that was locked a moment ago is a keyring
// that may be open now, and remembering the miss would keep a destination
// broken for the lifetime of the process after the user unlocked it.
func KeyringFetch(account string) (string, error) {
	secret, err := keyringGet(KeyringService, account)
	if err != nil {
		return "", err
	}
	keyringCacheMu.Lock()
	keyringCache[account] = secret
	keyringCacheMu.Unlock()
	return secret, nil
}

// KeyringForget removes one entry, and treats "there was no such entry" as
// success.
//
// ONLY EVER CALLED FROM AN EXPLICIT PATH — removing a destination, removing a
// snapshot schedule, renaming (the orphan under the old name), and `keychain
// disable`. State.Save never deletes anything: a save is the routine act of this
// program and a routine act that can destroy a password is how a password is
// destroyed. A keyring entry with nothing pointing at it wastes a line in
// Seahorse; a deleted one that turns out to have been needed is a share nobody
// can log into again.
func KeyringForget(account string) error {
	keyringCacheMu.Lock()
	delete(keyringCache, account)
	keyringCacheMu.Unlock()
	if err := keyringDelete(KeyringService, account); err != nil && !errors.Is(err, keyring.ErrNotFound) {
		return err
	}
	return nil
}

// ForgetKeyringCache drops everything this process remembers having seen, so the
// next KeyringPut really writes.
//
// FOR TESTS. The cache is process-wide by design (see keyringCache), and a test
// binary is one process standing in for many machines: a case that installs a
// fresh mock keyring would otherwise find its first write skipped because the
// previous case's value is still remembered, and would then read a store that
// never received it.
func ForgetKeyringCache() {
	keyringCacheMu.Lock()
	keyringCache = map[string]string{}
	keyringCacheMu.Unlock()
}

// keyringProbeAccount is the throwaway entry KeyringProbe round-trips. Named
// so that a user who catches sight of it in their keyring app can tell what it
// is; deleted immediately either way.
const keyringProbeAccount = "probe (not a stored password)"

// KeyringProbe answers "would this machine actually let us do this", by writing
// a value, reading it back and deleting it.
//
// A REAL ROUND TRIP RATHER THAN A FEATURE CHECK, because every way this fails is
// a way that only shows up when tried. There is no D-Bus session (a boot-started
// daemon, an ssh login on the Pi). D-Bus is there but no secret-service provider
// is (a minimal Debian install: the API exists, the answer is an error). The
// provider is there but its collection is locked, so a write prompts or refuses.
// On Windows the Credential Manager is present but a value can be too large.
// Compiled-in support tells you none of that.
//
// Called by `keychain enable` before anything is migrated, and by `keychain
// status`, so the answer somebody gets is about their machine and not about
// their operating system in general. Deliberately NOT called on the read path:
// LoadState runs constantly, and probing there would be two extra keyring calls
// per invocation to learn something the reads themselves are about to reveal.
func KeyringProbe() error {
	const canary = "backup-maker keyring probe"
	if err := keyringSet(KeyringService, keyringProbeAccount, canary); err != nil {
		return err
	}
	got, err := keyringGet(KeyringService, keyringProbeAccount)
	// Removed whatever happened, including when the read failed: a probe that
	// leaves litter behind is one nobody wants to run twice.
	_ = keyringDelete(KeyringService, keyringProbeAccount)
	if err != nil {
		return err
	}
	if got != canary {
		return fmt.Errorf("the keyring accepted a test entry but returned %q for it", got)
	}
	return nil
}

// KeyringWriteFallback is told when a secret could not be written to the OS
// keyring and was therefore left in state.json in plaintext instead.
//
// A HOOK RATHER THAN AN ERROR RETURN because the alternative to reporting is
// LOSING THE PASSWORD. Save's job at that moment is to keep the secret, which it
// does the way this program always did — 0600, machine-owned — and then to make
// sure the user is not under the impression that something else happened. It
// cannot fail the save over it: a failed save is a lost odometer, lost sync
// clocks and, on the wrong path, a lost password.
//
// A hook rather than a log line because this package is handed no logger; every
// component that logs here is given one by whoever built it. nil is the valid
// default and every call site tolerates it. Set once at startup, before anything
// can call Save.
var KeyringWriteFallback func(account string, err error)

func reportKeyringFallback(account string, err error) {
	if KeyringWriteFallback != nil {
		KeyringWriteFallback(account, err)
	}
}
