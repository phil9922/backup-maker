// SPDX-License-Identifier: MIT

package setup

import (
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"

	"github.com/phil9922/backup-maker/internal/config"
	"github.com/phil9922/backup-maker/internal/localmirror"
	"github.com/phil9922/backup-maker/internal/smbfs"
)

// DefaultShareTargetName is what a network destination is called when the user
// does not name it.
//
// IT NAMES THE DRIVE, NOT THE SHARE. The name that mattered was the bare share
// name, which is a fact about the server rather than about the storage: a box
// exporting one `backups` share with a directory per disk gave every disk on it
// the same default, and the second was refused outright. `//pi/backups/drive1`
// is `pi-drive1` — the machine it hangs off, and which drive.
//
// A BARE IP CONTRIBUTES NOTHING AND IS LEFT OUT. `192.168.1.50-backups` is
// longer, harder to read, and stops being true the day the DHCP lease moves —
// and this is a label somebody reads on a dashboard, not an address anything
// resolves. An address-addressed share falls back to naming the share and the
// drive: `//192.168.1.50/backups/drive1` is `backups-drive1`.
//
// Returns "" for an address it cannot parse; the caller is about to report that
// properly.
func DefaultShareTargetName(url string) string {
	host, _, share, subpath, err := smbfs.Parse(url)
	if err != nil {
		return ""
	}
	drive := share
	if subpath != "" {
		segs := strings.Split(strings.Trim(subpath, "/"), "/")
		drive = segs[len(segs)-1]
	}
	if net.ParseIP(host) != nil {
		if drive == share {
			return tidyName(share)
		}
		return tidyName(share + "-" + drive)
	}
	// The first label only: "pi.local" and "nas.home.arpa" are the machines
	// somebody calls "pi" and "nas", and the rest is how the network finds them.
	return tidyName(shortHost(host) + "-" + drive)
}

// shortHost is a hostname as a person says it.
func shortHost(host string) string {
	if i := strings.IndexByte(host, '.'); i > 0 {
		return host[:i]
	}
	return host
}

// tidyName makes a GENERATED name presentable. It only ever runs on a name the
// user did not type — one they typed is checked by ValidTargetName and refused
// rather than rewritten, for the reason ValidateFolderName gives.
func tidyName(name string) string {
	var b strings.Builder
	dash := false
	for _, r := range name {
		switch {
		case r == '/' || r == '\\' || r == ' ' || r == '\t' || r == ':' || r < 0x20:
			dash = true
		default:
			if dash && b.Len() > 0 {
				b.WriteByte('-')
			}
			dash = false
			b.WriteRune(r)
		}
	}
	return strings.Trim(b.String(), "-")
}

// FreeTargetName is base, or base with a number on the end if that is taken.
//
// A NAME THE USER DID NOT CHOOSE IS ADJUSTED, NOT REFUSED. This is the whole
// bug: adding a second disk from the same server produced the same default as
// the first, and the answer was "a target named "backups" already exists" with
// no suggestion of what would work. A default nobody typed is ours to move
// along. A name somebody DID type is refused instead (see AddShareTargetAs),
// because they asked for that exact one and quietly saving something else is
// worse than saying no.
//
// Compared case-insensitively even though the name is stored case-sensitively:
// two destinations called "backups" and "Backups" would work perfectly and be
// indistinguishable in every sentence anybody says about them.
func FreeTargetName(cfg *config.Config, base string) string {
	taken := func(name string) bool {
		for _, t := range cfg.Targets {
			if strings.EqualFold(t.Name, name) {
				return true
			}
		}
		return false
	}
	if !taken(base) {
		return base
	}
	// Bounded: an unbounded loop here would spin on a config that somehow holds
	// every candidate, and a hundred destinations of one name is not a case to
	// keep trying to serve.
	for n := 2; n <= 100; n++ {
		candidate := base + "-" + strconv.Itoa(n)
		if !taken(candidate) {
			return candidate
		}
	}
	return base
}

// TargetName decides what a destination is going to be called.
//
// ONE PLACE, because the decision has two halves that must not drift apart and
// three callers wanted it: a drive, a share, and the wizard's create-a-backup
// flow. A name the user TYPED is validated and refused if it is taken — they
// asked for that exact one, and saving something else under it is worse than
// saying no. A default is ours, so it is moved along instead of refusing.
func TargetName(cfg *config.Config, typed, fallback string) (string, error) {
	if typed == "" {
		return FreeTargetName(cfg, fallback), nil
	}
	if err := ValidTargetName(typed); err != nil {
		return "", err
	}
	if err := CheckNameFree(cfg, typed); err != nil {
		return "", err
	}
	return typed, nil
}

// ValidTargetName accepts a name somebody typed for a destination.
//
// The name is load-bearing beyond the label on a card: it keys the recorded
// marker UUID, the share password, every folder's last-synced clock, and any
// snapshot schedule pointing at it. So it has to be a name that survives being
// a key and being said out loud — but it is NOT a filesystem path (backups land
// under <machine>/<label>), which is why this is shorter than
// ValidateFolderName.
func ValidTargetName(name string) error {
	switch {
	case name == "":
		return errors.New("give the destination a name")
	case strings.TrimSpace(name) != name:
		return errors.New("a destination name cannot start or end with a space")
	case strings.ContainsAny(name, "/\\"):
		return errors.New("a destination name cannot contain a slash")
	case strings.ContainsRune(name, '\x00') || strings.ContainsAny(name, "\r\n\t"):
		return errors.New("a destination name cannot contain control characters")
	case len(name) > 64:
		return errors.New("a destination name that long will not fit anywhere it is shown; keep it under 64 characters")
	}
	return nil
}

// RenameTarget changes what a destination is called, everywhere it is called
// that.
//
// NOTHING ON ANY DESTINATION MOVES, and that is why this is safe to offer at
// all. The marker file on the storage holds a UUID and a machine name — never
// the target name — and the backups themselves live under <machine>/<label>,
// keyed by the FOLDER's label. So this is entirely a local re-keying, and it
// works with every destination unplugged.
//
// What it has to reach, and what happens if it misses one:
//
//   - config.toml: the target itself, any snapshot schedule aimed at it, and any
//     stopped folder's record of a copy sitting there.
//   - state.json: the marker UUID (miss it and the daemon says "target has no
//     recorded UUID; re-add it" and stops backing up there), the share password
//     (miss it and the user is asked for it again), and the per-folder
//     last-synced and last-pass records (miss them and every folder reads
//     "never synced" to that destination, so a drive nobody has plugged in for
//     months looks merely offline).
//
// TestRenamingADestinationLeavesNothingBehindUnderTheOldName is what keeps that
// list honest, and it fails when a new destination-keyed map is added to State.
func RenameTarget(from, to string) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	if err := ValidTargetName(to); err != nil {
		return err
	}
	if from == to {
		return nil
	}
	found := false
	for _, t := range cfg.Targets {
		if t.Name == from {
			found = true
		}
	}
	if !found {
		return fmt.Errorf("no destination named %q", from)
	}
	// The same refusal an ADD gets, suggesting a name that works — a rename into
	// a taken name is the same dead end this work exists to remove, and having
	// two different messages for one situation is how one of them stays wrong.
	if err := CheckNameFree(cfg, to); err != nil {
		return err
	}

	state, err := config.LoadState()
	if err != nil {
		return err
	}
	// REFUSED OUTRIGHT when this destination's password lives in the OS keyring
	// and the keyring would not give it up (locked, or no keyring session — see
	// internal/config/keyring.go).
	//
	// Everything below is a map move, and there is nothing in the map to move: the
	// rename would report success, config.toml would name the destination
	// "pi-drive1", and the password would still be filed in the keyring under
	// "share/backups" with nothing left on the machine pointing at it. That is
	// data loss, on a flow that looks like it worked, and it stays invisible until
	// the day the share stops logging in and the password nobody has written down
	// is asked for. Renaming is never urgent; unlocking a keyring is a moment's
	// work; so this waits.
	if state.SecretMissing(config.ShareKeyringAccount(from)) {
		return fmt.Errorf("%q keeps its password in the OS keyring, and the keyring is locked or unavailable right now: unlock it (or start a keyring session) and try again — renaming now would leave the password filed under the old name with nothing pointing at it. Check with: backup-maker keychain status", from)
	}
	// COPIED UNDER THE NEW NAME FIRST, and the old keys removed only after the
	// config is safely saved. A rename is two files, and a failure between them
	// is the one outcome that must not lose a destination: with both names
	// present in state.json, whichever file is the older one still finds the
	// UUID and the password it needs. The tidy-up afterwards is worth nothing on
	// its own, so its failure is not worth reporting.
	copyKey(state.DriveTargetUUIDs, from, to)
	copyKey(state.ShareCredentials, from, to)
	for _, byTarget := range state.MirrorLastSync {
		copyKey(byTarget, from, to)
	}
	for _, byTarget := range state.MirrorScanState {
		copyKey(byTarget, from, to)
	}
	if err := state.Save(); err != nil {
		return err
	}

	for i := range cfg.Targets {
		if cfg.Targets[i].Name == from {
			cfg.Targets[i].Name = to
		}
	}
	for i := range cfg.Archives {
		if cfg.Archives[i].Target == from {
			cfg.Archives[i].Target = to
		}
	}
	for i := range cfg.Retired {
		for j := range cfg.Retired[i].Copies {
			if cfg.Retired[i].Copies[j].Target == from {
				cfg.Retired[i].Copies[j].Target = to
			}
		}
	}
	if err := cfg.Save(); err != nil {
		return err
	}

	delete(state.DriveTargetUUIDs, from)
	delete(state.ShareCredentials, from)
	for _, byTarget := range state.MirrorLastSync {
		delete(byTarget, from)
	}
	for _, byTarget := range state.MirrorScanState {
		delete(byTarget, from)
	}
	_ = state.Save()

	// The orphaned keyring entry, last and best-effort. The entry under the NEW
	// name was already written by the first save above — putting secrets in the
	// keyring is what Save does — so by here the only thing left under the old
	// name is a duplicate nothing points at. Done after the saves rather than
	// before for the same reason the whole function copies before it deletes: a
	// failure at this point costs a stale line in the user's keyring app, whereas
	// the same failure earlier could cost the password.
	if state.SecretsInKeyring {
		_ = config.KeyringForget(config.ShareKeyringAccount(from))
	}
	return nil
}

// copyKey duplicates one entry under a new key, leaving the old one in place.
// Absent keys are left absent rather than written as a zero value, which for
// the share password would be an empty password saved as if it were real.
func copyKey[V any](m map[string]V, from, to string) {
	if m == nil {
		return
	}
	if v, ok := m[from]; ok {
		m[to] = v
	}
}

// DescribeTarget records which physical drive a destination is, in the user's
// own words, by writing it into that destination's marker file.
//
// ONTO THE STORAGE, WHICH IS WHY IT CAN FAIL. A description in config.toml would
// always save and would answer nothing: the question is "which of these two
// identical cards is this?", asked by somebody holding a card, and the volume
// label cannot be read over SMB at all (see localmirror.Marker.Description). So
// this needs the destination present, and says so plainly when it is not.
//
// The marker is read and rewritten rather than replaced, so the UUID that
// identifies the storage survives — replacing it would make this machine refuse
// its own drive as foreign the moment the description was saved.
func DescribeTarget(name, description string) error {
	description = strings.TrimSpace(description)
	if len(description) > 200 {
		return errors.New("keep the description under 200 characters — it sits beside the destination's name")
	}
	if strings.ContainsAny(description, "\r\n") {
		return errors.New("a description is one line")
	}
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	for _, t := range cfg.Targets {
		if t.Name != name {
			continue
		}
		if t.Type == "device" {
			return fmt.Errorf("%q is a paired computer: it has no drive of ours to describe, because it runs its own backup-maker and manages its own storage", name)
		}
		src := AdoptSource{Path: t.Path, URL: t.URL, Username: t.Username}
		if t.Type == "share" {
			state, err := config.LoadState()
			if err != nil {
				return err
			}
			src.Password = state.ShareCredentials[name]
		}
		b, err := src.open()
		if err != nil {
			return fmt.Errorf("cannot reach %s to write the description onto it: %w", name, err)
		}
		defer b.Close()
		if err := localmirror.Describe(b, description); err != nil {
			return fmt.Errorf("writing the description onto %s: %w", name, err)
		}
		return nil
	}
	return fmt.Errorf("no destination named %q", name)
}
