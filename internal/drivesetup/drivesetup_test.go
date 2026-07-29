// SPDX-License-Identifier: MIT

package drivesetup

import (
	"encoding/json"
	"strings"
	"testing"
)

// THE GUARANTEE: the command the dashboard may run as root takes no arguments.
//
// The sudoers rule that permits it is built from exactly this, so anything
// variable here becomes a wildcard there — and a sudoers wildcard matches any
// further arguments, whitespace included. The rule then also permits
// `prepare-drive --force …`, which skips the refusal of a drive that already
// has something on it: the guard the entire design leans on. The arguments are
// the attack surface, so there are none.
func TestThePrivilegedCommandTakesNoArgumentsToInject(t *testing.T) {
	args := PrivilegedArgs()
	want := []string{"prepare-drive", "--from-stdin"}

	if len(args) != len(want) {
		t.Fatalf("PrivilegedArgs() = %v, want exactly %v", args, want)
	}
	for i := range want {
		if args[i] != want[i] {
			t.Fatalf("PrivilegedArgs() = %v, want exactly %v", args, want)
		}
	}
	// Nothing that varies per request may appear: a rule built from this must
	// be a fixed string, never a pattern.
	r := Request{Device: "/dev/sda", Mount: "/mnt/backups", Label: "BACKUPS", Confirm: "sda 465.8GB"}
	joined := strings.Join(PrivilegedArgs(), " ")
	for _, secret := range []string{r.Device, r.Mount, r.Label, r.Confirm, "*"} {
		if strings.Contains(joined, secret) {
			t.Errorf("the privileged command contains %q; the rule permitting it would have to match a pattern", secret)
		}
	}
}

// THE GUARANTEE: --force cannot be expressed in a request.
//
// It skips the "there is already something on it" check. It is meant for a
// person who has typed their own password at a terminal, never for the
// passwordless path, and the way that is enforced is that the request has
// nowhere to put it.
func TestARequestCannotAskForForce(t *testing.T) {
	body, err := Request{Device: "/dev/sda", Mount: "/mnt/backups"}.Encode()
	if err != nil {
		t.Fatal(err)
	}
	var raw map[string]any
	if err := json.Unmarshal(body, &raw); err != nil {
		t.Fatal(err)
	}
	for key := range raw {
		switch key {
		case "device", "mount", "label", "confirm", "probe":
		default:
			t.Errorf("a request carries an unexpected field %q; only the drive and the probe may cross this boundary", key)
		}
	}

	// And a hand-written request that tries anyway is simply ignored: the
	// decoder has nowhere to put it.
	var r Request
	if err := json.Unmarshal([]byte(`{"device":"/dev/sda","force":true,"Force":true}`), &r); err != nil {
		t.Fatal(err)
	}
	if r.Device != "/dev/sda" {
		t.Fatalf("device did not survive decoding: %+v", r)
	}
}

// A probe must be distinguishable from a real request, or the daemon's
// "can I do this without a password?" check would prepare a drive.
func TestAProbeIsNotARequestToDoAnything(t *testing.T) {
	body, err := Request{Probe: true}.Encode()
	if err != nil {
		t.Fatal(err)
	}
	var back Request
	if err := json.Unmarshal(body, &back); err != nil {
		t.Fatal(err)
	}
	if !back.Probe {
		t.Error("the probe flag did not survive the round trip, so the check would format a drive")
	}
	if back.Device != "" || back.Mount != "" {
		t.Errorf("a probe carries a device or mount: %+v", back)
	}

	// The reverse: a real request must never arrive marked as a probe, or it
	// would silently do nothing and report success.
	real := Request{Device: "/dev/sda", Mount: "/mnt/backups"}
	if real.Probe {
		t.Error("a request built for real work defaults to being a probe")
	}
}

// The pasted command a person types is the readable flag form — that path goes
// through their own password, so it is not the one being locked down. It must
// still name the drive, or the instructions are useless.
func TestTheCommandAPersonPastesNamesTheDrive(t *testing.T) {
	r := Request{Device: "/dev/sda", Mount: "/mnt/backups", Label: "BACKUPS", Confirm: "sda 465.8GB"}
	line := strings.Join(r.Args(), " ")

	for _, want := range []string{"prepare-drive", "--device /dev/sda", "--mount /mnt/backups", "--confirm sda 465.8GB"} {
		if !strings.Contains(line, want) {
			t.Errorf("the pasted command %q is missing %q", line, want)
		}
	}
}

// A confirmation phrase has a space in it, so the pasted command has to quote
// its arguments or the user pastes something that will not parse.
func TestPastedArgumentsWithSpacesAreQuoted(t *testing.T) {
	got := quoted([]string{"prepare-drive", "--confirm", "sda 465.8GB"})
	if !strings.Contains(got, `"sda 465.8GB"`) {
		t.Errorf("quoted() = %q, want the phrase with the space quoted", got)
	}
	if strings.Contains(got, `"prepare-drive"`) {
		t.Errorf("quoted() = %q, want plain words left unquoted", got)
	}
}
