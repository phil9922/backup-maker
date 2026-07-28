// SPDX-License-Identifier: MIT

package localmirror

import (
	"os"
	"path/filepath"
	"testing"
)

// ownedBy makes the Owns predicate for one installation.
func ownedBy(ids ...string) func(string) bool {
	return func(id string) bool {
		for _, want := range ids {
			if id != "" && id == want {
				return true
			}
		}
		return false
	}
}

func engineFor(t *testing.T, src, dst, machine, label, installID string, owns func(string) bool) *Engine {
	t.Helper()
	return New(Options{
		FolderID: label + "-id", TargetName: "card", SourcePath: src,
		Backend: NewLocalFS(dst), MachineName: machine, Label: label,
		UUID: "uuid-1", MaxAgeDays: 30, Log: quietLog(),
		InstallID: installID, Owns: owns,
	})
}

// THE FAILURE THIS PREVENTS, and it is the worst kind this program has: two
// computers whose machine_name is the same — it defaults to the hostname, and
// two fresh installs of one distro image really are both "ubuntu" — file their
// backups under the same <machine>/<label> directory on a drive they share.
// Each one's reconcile pass then finds the other's files, sees content that is
// not in its own source, and versions it away. Both dashboards report a healthy
// backup throughout.
//
// The assertion that matters is the last one: the first machine's file is still
// there, byte for byte.
func TestAMirrorRefusesADirectoryClaimedByAnotherComputer(t *testing.T) {
	dst := t.TempDir()
	if err := WriteMarkerAt(dst, "uuid-1", "ubuntu"); err != nil {
		t.Fatal(err)
	}

	firstSrc := t.TempDir()
	mustWrite(t, filepath.Join(firstSrc, "irreplaceable.txt"), "the only copy")
	first := engineFor(t, firstSrc, dst, "ubuntu", "docs", "install-first", ownedBy("install-first"))
	first.sync()

	backup := filepath.Join(dst, "ubuntu", "docs", "irreplaceable.txt")
	before, err := os.ReadFile(backup)
	if err != nil {
		t.Fatalf("the first machine's backup was not made, so this test proves nothing: %v", err)
	}

	// The second computer: same name, entirely different files.
	secondSrc := t.TempDir()
	mustWrite(t, filepath.Join(secondSrc, "something-else.txt"), "unrelated")
	second := engineFor(t, secondSrc, dst, "ubuntu", "docs", "install-second", ownedBy("install-second"))
	second.sync()
	second.sync()

	if st := second.Status(); st.State != "name-clash" {
		t.Errorf("the second computer reported %q; a state of its own is what makes this visible rather than looking like an ordinary fault", st.State)
	}

	after, err := os.ReadFile(backup)
	if err != nil {
		t.Fatalf("THE FIRST MACHINE'S ONLY BACKUP WAS DELETED by a second computer sharing its name: %v", err)
	}
	if string(after) != string(before) {
		t.Fatalf("the first machine's backup was overwritten: got %q, want %q", after, before)
	}
	if _, err := os.Stat(filepath.Join(dst, "ubuntu", "docs", "something-else.txt")); err == nil {
		t.Error("the second computer wrote into a directory it does not own")
	}

	// The first machine carries on unaffected — refusing the second must not
	// cost the one that was there first.
	mustWrite(t, filepath.Join(firstSrc, "later.txt"), "added afterwards")
	first.sync()
	if st := first.Status(); st.State != "in sync" {
		t.Errorf("the machine that owns the directory reported %q, want in sync", st.State)
	}
	if _, err := os.Stat(filepath.Join(dst, "ubuntu", "docs", "later.txt")); err != nil {
		t.Errorf("the owning machine stopped backing up: %v", err)
	}
}

// The upgrade path. Every destination in service before claims existed has no
// claim file on it, and refusing those would mean replacing the binary silently
// stopped every working backup on the machine.
func TestAnUnclaimedDirectoryIsAdoptedRatherThanRefused(t *testing.T) {
	dst := t.TempDir()
	if err := WriteMarkerAt(dst, "uuid-1", "my-laptop"); err != nil {
		t.Fatal(err)
	}
	// A year of backups and no claim file: exactly what the live card looks
	// like the moment a new binary starts.
	if err := os.MkdirAll(filepath.Join(dst, "my-laptop", "docs"), 0o755); err != nil {
		t.Fatal(err)
	}
	mustWrite(t, filepath.Join(dst, "my-laptop", "docs", "old.txt"), "backed up months ago")

	src := t.TempDir()
	mustWrite(t, filepath.Join(src, "old.txt"), "backed up months ago")
	mustWrite(t, filepath.Join(src, "new.txt"), "added today")
	e := engineFor(t, src, dst, "my-laptop", "docs", "install-laptop", ownedBy("install-laptop"))
	e.sync()

	if st := e.Status(); st.State != "in sync" {
		t.Fatalf("an upgrade turned a working backup into %q", st.State)
	}
	if _, err := os.Stat(filepath.Join(dst, "my-laptop", "docs", "new.txt")); err != nil {
		t.Errorf("the mirror stopped copying after the upgrade: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dst, "my-laptop", ClaimName)); err != nil {
		t.Errorf("the directory was used but never claimed, so the next machine along could take it: %v", err)
	}
}

// Adopting a destination and continuing as the machine that wrote it means
// taking on its identity — including the claims it left on destinations that
// were not plugged in at the time. Without this a correctly restored computer
// would refuse to back up to its own drives.
func TestContinuingAsTheOldMachineKeepsItsClaim(t *testing.T) {
	dst := t.TempDir()
	if err := WriteMarkerAt(dst, "uuid-1", "oldbox"); err != nil {
		t.Fatal(err)
	}
	src := t.TempDir()
	mustWrite(t, filepath.Join(src, "notes.txt"), "carried over")

	// The old machine claimed it, then died.
	old := engineFor(t, src, dst, "oldbox", "docs", "install-old", ownedBy("install-old"))
	old.sync()

	// The rebuilt machine: new install id, but it inherited the old one when it
	// adopted the drive and chose to continue as "oldbox".
	rebuilt := engineFor(t, src, dst, "oldbox", "docs", "install-new", ownedBy("install-new", "install-old"))
	rebuilt.sync()

	if st := rebuilt.Status(); st.State != "in sync" {
		t.Fatalf("an adopted machine was locked out of its own backups: state %q", st.State)
	}

	// A machine that did NOT inherit it is still refused — inheritance must be
	// the specific thing adoption grants, not a hole anyone can walk through.
	stranger := engineFor(t, src, dst, "oldbox", "docs", "install-stranger", ownedBy("install-stranger"))
	stranger.sync()
	if st := stranger.Status(); st.State != "name-clash" {
		t.Errorf("a machine that never adopted this drive was allowed in: state %q", st.State)
	}
}

// A claim file that cannot be parsed is treated as somebody else's, never as
// ours — the same way a marker that cannot be matched is. The cost of refusing
// wrongly is a message on screen; the cost of accepting wrongly is somebody's
// files.
func TestACorruptClaimIsRefusedRatherThanIgnored(t *testing.T) {
	dst := t.TempDir()
	if err := WriteMarkerAt(dst, "uuid-1", "ubuntu"); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dst, "ubuntu"), 0o755); err != nil {
		t.Fatal(err)
	}
	mustWrite(t, filepath.Join(dst, "ubuntu", ClaimName), "{ this is not json")

	src := t.TempDir()
	mustWrite(t, filepath.Join(src, "notes.txt"), "private")
	e := engineFor(t, src, dst, "ubuntu", "docs", "install-mine", ownedBy("install-mine"))
	e.sync()

	if st := e.Status(); st.State != "name-clash" {
		t.Errorf("a claim file that could not be read was treated as absent: state %q", st.State)
	}
	if _, err := os.Stat(filepath.Join(dst, "ubuntu", "docs", "notes.txt")); err == nil {
		t.Error("files were written into a directory whose ownership could not be established")
	}
}

// An engine built without an identity behaves exactly as it did before claims
// existed. Being unable to identify ourselves is not a reason to stop backing
// up, and every existing test constructs engines this way.
func TestAnEngineWithNoIdentityBacksUpAsBefore(t *testing.T) {
	dst := t.TempDir()
	if err := WriteMarkerAt(dst, "uuid-1", "workstation"); err != nil {
		t.Fatal(err)
	}
	src := t.TempDir()
	mustWrite(t, filepath.Join(src, "notes.txt"), "private")

	e := New(Options{
		FolderID: "f1", TargetName: "card", SourcePath: src,
		Backend: NewLocalFS(dst), MachineName: "workstation", Label: "docs",
		UUID: "uuid-1", MaxAgeDays: 30, Log: quietLog(),
	})
	e.sync()

	if st := e.Status(); st.State != "in sync" {
		t.Fatalf("state = %q, want in sync", st.State)
	}
	if _, err := os.Stat(filepath.Join(dst, "workstation", "docs", "notes.txt")); err != nil {
		t.Errorf("nothing was backed up: %v", err)
	}
}
