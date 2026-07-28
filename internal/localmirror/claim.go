// SPDX-License-Identifier: MIT

package localmirror

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"path"
	"time"
)

// ClaimName sits inside <root>/<machine>/ and records which INSTALLATION that
// directory belongs to.
//
// IT ANSWERS A DIFFERENT QUESTION FROM THE MARKER, and both have to be asked.
// MarkerName one level up says "this is the right storage" — and it says yes to
// every computer that shares a drive, which is correct and is the point. This
// says "this directory on it is mine".
//
// Without it, two computers whose machine_name happens to match — it defaults
// to the hostname, and two fresh installs of one distro image really are both
// "ubuntu" — file their backups under the same <machine>/<label> directory.
// Each one's reconcile pass then finds the other's files, sees content that is
// not in its own source, and versions it away. Nothing about that is visible:
// both dashboards report a healthy backup while each quietly dismantles the
// other's.
//
// Keyed on an install id rather than a machine name because the machine name is
// exactly the thing that collided. Kept in its own small file rather than
// folded into the manifest beside it, because it is read on the write path of
// every destination and must not depend on a larger derived document that a
// share can fail to write.
const ClaimName = ".backup-maker-machine.json"

// Claim is the contents of that file. The machine name is recorded for the
// benefit of a person reading it, and of the message shown when two computers
// collide; nothing decides anything by it.
type Claim struct {
	InstallID   string    `json:"install_id"`
	MachineName string    `json:"machine_name"`
	Claimed     time.Time `json:"claimed"`
}

// ClaimState is what the directory turns out to be.
type ClaimState int

const (
	// ClaimOurs: the claim is there and this installation holds it. The only
	// state in which anything may be written into the directory.
	ClaimOurs ClaimState = iota
	// ClaimUnclaimed: no claim file — either the directory does not exist yet,
	// or it predates claims entirely. Who may take it is a decision for the
	// caller, and the answer deliberately differs between the daemon and setup.
	ClaimUnclaimed
	// ClaimOther: a different installation holds it.
	ClaimOther
	// ClaimUnreadable: a claim file that cannot be parsed. Treated as ClaimOther
	// and never as ours, the same way checkPresence treats a marker it cannot
	// match: the cost of refusing wrongly is an error message, and the cost of
	// accepting wrongly is somebody's files.
	ClaimUnreadable
)

// CheckClaim classifies a machine directory against the installation asking.
// owns reports whether an install id is this installation's, counting ids
// inherited by adoption — see config.State.Owns.
func CheckClaim(b Backend, machineDir string, owns func(string) bool) (ClaimState, *Claim) {
	data, err := b.ReadFile(path.Join(machineDir, ClaimName))
	if err != nil {
		return ClaimUnclaimed, nil
	}
	var c Claim
	if err := json.Unmarshal(data, &c); err != nil || c.InstallID == "" {
		return ClaimUnreadable, nil
	}
	if owns != nil && owns(c.InstallID) {
		return ClaimOurs, &c
	}
	return ClaimOther, &c
}

// WriteClaim stamps a machine directory as belonging to one installation,
// creating the directory if it is not there yet.
func WriteClaim(b Backend, machineDir, installID, machineName string) error {
	if installID == "" {
		return fmt.Errorf("refusing to claim %q with no install id", machineDir)
	}
	if err := b.MkdirAll(machineDir); err != nil {
		return err
	}
	data, err := json.MarshalIndent(Claim{
		InstallID:   installID,
		MachineName: machineName,
		Claimed:     time.Now(),
	}, "", "  ")
	if err != nil {
		return err
	}
	return b.WriteFile(path.Join(machineDir, ClaimName), data)
}

// ClaimIfUnclaimed takes a machine directory only if nobody holds it, and
// reports what the directory turned out to be.
//
// IT READS THE CLAIM BACK AND COMPARES. Two computers reaching an unclaimed
// directory at the same moment would otherwise both write, both succeed, and
// both conclude they own it — which is the exact state this whole mechanism
// exists to make impossible. Reading back means the loser sees the winner's id
// and refuses. It is not a lock and cannot be one over SMB; it is a
// last-writer-visible check, which closes the window from "indefinite" to "the
// gap between two writes", and every subsequent pass agrees.
func ClaimIfUnclaimed(b Backend, machineDir, installID, machineName string, owns func(string) bool) (ClaimState, error) {
	switch st, _ := CheckClaim(b, machineDir, owns); st {
	case ClaimOurs:
		return ClaimOurs, nil
	case ClaimOther, ClaimUnreadable:
		return st, nil
	}
	if err := WriteClaim(b, machineDir, installID, machineName); err != nil {
		return ClaimUnclaimed, err
	}
	st, _ := CheckClaim(b, machineDir, owns)
	return st, nil
}

// MachineDirIsInUse reports whether a machine directory already holds backups —
// asked when there is no claim on it, to tell "a directory this machine has
// been writing to since before claims existed" from "an empty name nobody has
// taken". The two need opposite answers and the difference is not otherwise
// visible.
func MachineDirIsInUse(b Backend, machineDir string) bool {
	if _, err := b.Stat(machineDir); err != nil {
		return false
	}
	inUse := false
	_ = b.WalkDir(machineDir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || p == machineDir || p == "" || p == "." {
			return nil
		}
		// A mirrored folder is a directory. Loose files at this level are our
		// own — the claim, the manifest, the status page — and must not read as
		// somebody's backups.
		if d != nil && d.IsDir() {
			inUse = true
			return fs.SkipAll
		}
		return nil
	})
	return inUse
}
