// SPDX-License-Identifier: MIT

package setup

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/phil9922/backup-maker/internal/config"
	"github.com/phil9922/backup-maker/internal/localmirror"
)

// claimedBy stamps a destination as another installation's, the way that
// machine's own setup would have.
func claimedBy(t *testing.T, root, machine, installID string) {
	t.Helper()
	b := localmirror.NewLocalFS(root)
	if err := localmirror.WriteClaim(b, config.MachineDir(machine), installID, machine); err != nil {
		t.Fatalf("seeding another machine's claim: %v", err)
	}
}

// Setup is where a person is present to be asked, so this is where the collision
// is caught before any configuration is written — rather than leaving it to the
// daemon, which would accept the destination, then quietly refuse to write to it.
func TestAddingADestinationAnotherComputerAlreadyUsesIsRefusedAtSetupTime(t *testing.T) {
	isolate(t)
	root := t.TempDir()
	claimedBy(t, root, "ubuntu", "install-theirs")

	cfg := config.New()
	cfg.General.MachineName = "ubuntu"

	_, err := AppendDriveTarget(cfg, root, "card")
	if err == nil {
		t.Fatal("a destination another computer already uses under this name was accepted")
	}
	var conflict *ClaimConflictError
	if !errors.As(err, &conflict) {
		t.Fatalf("got a plain error the wizard cannot offer a choice for: %v", err)
	}
	if conflict.MachineName != "ubuntu" {
		t.Errorf("the conflict does not name the colliding name: %+v", conflict)
	}
	if conflict.Legacy {
		t.Error("a directory another install actively holds was reported as the softer legacy case")
	}
	if len(cfg.Targets) != 0 {
		t.Error("the target was added to the configuration despite the refusal")
	}
	// The remedy must be in the message: a bare refusal leaves somebody stuck.
	if msg := conflict.Error(); !strings.Contains(msg, "folder of its own") {
		t.Errorf("the error does not say what to do instead: %q", msg)
	}
}

// Taking a name over is a decision, never an inference — it means one computer
// starts writing into a tree another one is reconciling against a different
// source.
func TestTakingOverANameIsOnlyEverAnExplicitChoice(t *testing.T) {
	isolate(t)
	root := t.TempDir()
	claimedBy(t, root, "ubuntu", "install-theirs")

	cfg := config.New()
	cfg.General.MachineName = "ubuntu"

	if _, err := AppendDriveTargetIn(cfg, root, "card", false, false); err == nil {
		t.Fatal("the name was taken over without anyone asking for it")
	}
	if _, err := AppendDriveTargetIn(cfg, root, "card", false, true); err != nil {
		t.Fatalf("an explicit take-over was refused: %v", err)
	}
	// And the claim really did change hands, or the daemon would refuse the
	// destination the setup just accepted.
	state, err := config.LoadState()
	if err != nil {
		t.Fatal(err)
	}
	st, _ := localmirror.CheckClaim(localmirror.NewLocalFS(root), "ubuntu", state.Owns)
	if st != localmirror.ClaimOurs {
		t.Errorf("after a take-over the directory is still not ours: %v", st)
	}
}

// The upgrade case, and the reason setup asks rather than assuming: a folder
// that holds backups but carries no claim is either this machine's own work
// from before claims existed, or another computer's. From here they are
// genuinely indistinguishable.
func TestAFolderThatAlreadyHoldsBackupsAsksBeforeItIsUsed(t *testing.T) {
	isolate(t)
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "my-laptop", "Development"), 0o755); err != nil {
		t.Fatal(err)
	}

	cfg := config.New()
	cfg.General.MachineName = "my-laptop"

	_, err := AppendDriveTarget(cfg, root, "card")
	var conflict *ClaimConflictError
	if !errors.As(err, &conflict) {
		t.Fatalf("existing backups under this name were adopted without asking: %v", err)
	}
	if !conflict.Legacy {
		t.Error("an unclaimed folder was reported as another computer's, which overstates what is known")
	}

	// Confirmed, it is used and claimed — and the backups already there are left
	// exactly as they were.
	if _, err := AppendDriveTargetIn(cfg, root, "card", false, true); err != nil {
		t.Fatalf("confirming was refused: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "my-laptop", "Development")); err != nil {
		t.Errorf("the backups already there were disturbed: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "my-laptop", localmirror.ClaimName)); err != nil {
		t.Errorf("the folder was used but not claimed: %v", err)
	}
}

// An empty destination is the ordinary first-run case and must stay silent.
func TestAnEmptyDestinationIsClaimedWithoutAsking(t *testing.T) {
	isolate(t)
	root := t.TempDir()

	cfg := config.New()
	cfg.General.MachineName = "my-laptop"

	if _, err := AppendDriveTarget(cfg, root, "card"); err != nil {
		t.Fatalf("setting up a blank drive asked a question it did not need to: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "my-laptop", localmirror.ClaimName)); err != nil {
		t.Errorf("a blank drive was not claimed: %v", err)
	}
}

// Two computers, two folders, one drive — the arrangement the whole change
// exists to make possible.
func TestTwoComputersCanShareADriveThroughFoldersOfTheirOwn(t *testing.T) {
	isolate(t)
	drive := t.TempDir()

	first := config.New()
	first.General.MachineName = "ubuntu"
	if _, err := AppendDriveTargetIn(first, filepath.Join(drive, "desktop"), "card", true, false); err != nil {
		t.Fatalf("the first computer could not take a folder on the drive: %v", err)
	}

	second := config.New()
	second.General.MachineName = "ubuntu" // the same name, deliberately
	if _, err := AppendDriveTargetIn(second, filepath.Join(drive, "laptop"), "card2", true, false); err != nil {
		t.Fatalf("the second computer was refused a folder of its own: %v", err)
	}

	// Separate roots, each with its own marker: two destinations, not one.
	for _, name := range []string{"desktop", "laptop"} {
		if _, err := os.Stat(filepath.Join(drive, name, localmirror.MarkerName)); err != nil {
			t.Errorf("%s is not a destination in its own right: %v", name, err)
		}
	}
	// And nothing was written at the drive's own root.
	if _, err := os.Stat(filepath.Join(drive, localmirror.MarkerName)); err == nil {
		t.Error("the drive root was stamped even though the destinations are folders on it")
	}
}

// The refusal is the one message somebody meets while their backup is not
// running. Describing the remedy without giving it leaves them guessing at flag
// names, so the error carries the exact commands.
func TestTheRefusalPrintsCommandsRatherThanDescribingThem(t *testing.T) {
	isolate(t)
	root := t.TempDir()
	claimedBy(t, root, "ubuntu", "install-theirs")

	cfg := config.New()
	cfg.General.MachineName = "ubuntu"

	_, err := AppendDriveTarget(cfg, root, "card")
	var c *ClaimConflictError
	if !errors.As(err, &c) {
		t.Fatalf("expected a claim conflict, got: %v", err)
	}
	if c.Location != root || c.Kind != "drive" {
		t.Fatalf("the error does not know where the destination is: kind=%q location=%q", c.Kind, c.Location)
	}

	r := c.Remedy()
	for _, want := range []string{
		"backup-maker add-target drive " + root + "/ubuntu-2 --create",
		"backup-maker add-target drive " + root + " --take-over",
	} {
		if !strings.Contains(r, want) {
			t.Errorf("the remedy does not offer:\n  %s\ngot:\n%s", want, r)
		}
	}
	// The suggested folder must not be the name that just collided.
	if strings.Contains(r, root+"/ubuntu ") {
		t.Error("the remedy suggests the very name that collided")
	}
}

// The softer case has a different answer — there is nobody to take the name
// from, only a confirmation to give — so it must not offer a folder as though
// somebody else held the name.
func TestTheLegacyRefusalOffersOnlyTheConfirmation(t *testing.T) {
	isolate(t)
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "my-laptop", "Development"), 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := config.New()
	cfg.General.MachineName = "my-laptop"

	_, err := AppendDriveTarget(cfg, root, "card")
	var c *ClaimConflictError
	if !errors.As(err, &c) {
		t.Fatalf("expected a claim conflict, got: %v", err)
	}
	r := c.Remedy()
	if !strings.Contains(r, "--take-over") {
		t.Errorf("no way to confirm these are this computer's backups:\n%s", r)
	}
	if strings.Contains(r, "--create") {
		t.Errorf("a folder was offered for a name nobody else holds:\n%s", r)
	}
}

// A conflict raised without a recorded location must print nothing rather than
// half a command line.
func TestARemedyWithNoLocationIsSilent(t *testing.T) {
	c := &ClaimConflictError{MachineName: "ubuntu"}
	if r := c.Remedy(); r != "" {
		t.Errorf("printed a command with no destination in it: %q", r)
	}
}
