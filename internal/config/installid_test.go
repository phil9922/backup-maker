// SPDX-License-Identifier: MIT

package config

import (
	"os"
	"testing"
)

// The install id is what tells two computers that happen to share a machine
// name apart on a destination they both use. If it were re-minted — on a
// restart, on a state rewrite, on the second call — every claim this machine
// holds would stop reading as its own, and the mirror would refuse to write to
// drives it has been backing up to for months.

func TestAFreshInstallMintsOneStableInstallID(t *testing.T) {
	dir := pointConfigDirInto(t, t.TempDir())
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}

	first, err := EnsureInstallID()
	if err != nil {
		t.Fatalf("EnsureInstallID: %v", err)
	}
	if first == "" {
		t.Fatal("a fresh install was given no install id at all")
	}

	second, err := EnsureInstallID()
	if err != nil {
		t.Fatalf("EnsureInstallID (second call): %v", err)
	}
	if second != first {
		t.Fatalf("install id was re-minted on the second call: %q then %q", first, second)
	}

	// And it is on disk, not merely in the returned value: the CLI and the
	// daemon are different processes and must agree about who this machine is.
	s, err := LoadState()
	if err != nil {
		t.Fatalf("LoadState: %v", err)
	}
	if s.InstallID != first {
		t.Fatalf("state.json holds %q, but EnsureInstallID returned %q", s.InstallID, first)
	}
}

func TestAnInheritedInstallIDStillReadsAsOurs(t *testing.T) {
	// Adopting a destination and continuing as the machine that wrote it means
	// taking on its identity. Claims it left on destinations that were not
	// plugged in at the time must still read as ours when they reappear.
	s := &State{InstallID: "new-machine", InheritedInstallIDs: []string{"old-machine"}}

	if !s.Owns("new-machine") {
		t.Error("a machine does not recognise its own install id")
	}
	if !s.Owns("old-machine") {
		t.Error("an install id inherited by adoption does not read as ours, so an adopted machine would refuse to back up to its own drives")
	}
	if s.Owns("somebody-else") {
		t.Error("a stranger's install id reads as ours, which is the collision this whole mechanism exists to prevent")
	}
	if s.Owns("") {
		t.Error("an empty install id reads as ours; an unclaimed or corrupt claim must never be mistaken for our own")
	}
}
