// SPDX-License-Identifier: MIT

package setup

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

// ClaimConflictError is returned when a destination's folder for this computer
// already belongs to a different one.
//
// IT CARRIES ENOUGH TO OFFER A CHOICE rather than a dead end. The remedy is
// easy and not guessable — give this computer its own folder on that storage —
// and a bare "refused" would leave somebody either giving up or, worse, finding
// their own way to force it.
//
// Renaming the computer is deliberately not among the remedies. It would orphan
// everything already backed up under the old name and re-copy the lot, which is
// not a thing to suggest in an error message.
type ClaimConflictError struct {
	// MachineName is the name both computers are using — the collision itself.
	MachineName string
	// TargetName is the destination being set up, in this machine's words.
	TargetName string
	// Claimed is when the other installation took the name. Zero for Legacy.
	Claimed time.Time
	// Legacy marks the softer case: nobody has claimed the folder, but it
	// already holds backups. That is either this machine's own work from before
	// claims existed, or another computer's — and from here the two are
	// genuinely indistinguishable, so the user is asked rather than guessed at.
	Legacy bool
	// Location and Kind are where the destination is and what sort it is
	// ("drive" or "share"), filled in by whichever caller knows — the marker
	// check itself only ever holds an opened backend. They exist so Remedy can
	// print commands somebody can paste rather than a description of them.
	Location string
	Kind     string
}

// Remedy is the CLI's second half of the message: the exact commands, not a
// description of them.
//
// SEPARATE FROM Error() BECAUSE THE TWO CALLERS NEED DIFFERENT THINGS. The
// browser renders the same conflict as buttons, and shell commands underneath
// them would be noise; a terminal has no buttons, and "give this computer a
// folder of its own" without the command is exactly the sort of instruction
// that leaves somebody guessing at flag names while their backup is not
// running. Empty when the caller did not record a location, so nothing prints a
// half-finished command line.
func (e *ClaimConflictError) Remedy() string {
	if e.Location == "" {
		return ""
	}
	if e.Legacy {
		return "If those backups are this computer's, confirm it:\n" +
			"    backup-maker add-target " + e.Kind + " " + e.Location + " --take-over"
	}
	var own string
	switch e.Kind {
	case "drive":
		own = "    backup-maker add-target drive " +
			strings.TrimSuffix(e.Location, "/") + "/" + suggestFolder(e.MachineName) + " --create"
	case "share":
		// No --create for a share: the folder has to be made on the server that
		// hosts it, and inventing one over SMB from here would be a surprise on
		// somebody else's machine.
		own = "    backup-maker add-target share " +
			strings.TrimSuffix(e.Location, "/") + "/" + suggestFolder(e.MachineName) +
			"\n    (make that folder on the share first)"
	default:
		return ""
	}
	return "Give this computer a folder of its own there:\n" + own +
		"\nOr, if this IS that computer reinstalled, take the name over:\n" +
		"    backup-maker add-target " + e.Kind + " " + e.Location + " --take-over"
}

// suggestFolder proposes a folder name for the refused computer. The machine
// name alone is no good — it is the thing that collided — so it is suffixed,
// which is both honest about why and something the user can obviously change.
func suggestFolder(machineName string) string {
	name := strings.TrimSpace(machineName)
	if name == "" {
		return "this-computer"
	}
	return name + "-2"
}

func (e *ClaimConflictError) Error() string {
	if e.Legacy {
		return fmt.Sprintf(
			"that storage already holds backups filed under the name %q. "+
				"If those are this computer's, confirm and it will carry on using them. "+
				"If they belong to another computer, give this one a folder of its own "+
				"on that storage instead — sharing the name would make the two "+
				"delete each other's files",
			e.MachineName)
	}
	when := ""
	if !e.Claimed.IsZero() {
		when = " on " + e.Claimed.Format("2 Jan 2006")
	}
	return fmt.Sprintf(
		"another computer is already backing up to that storage under the name %q%s. "+
			"Backing up there under the same name would make the two computers "+
			"delete each other's files. Give this computer a folder of its own on "+
			"that storage, or — if this IS that computer, reinstalled — take the name over",
		e.MachineName, when)
}

// Detail lets the dashboard answer with a status and a body the wizard can
// render as buttons.
//
// AN INTERFACE RATHER THAN AN IMPORT: internal/webui deliberately does not
// import internal/setup, and this is the one error where "what went wrong" is
// not enough — the caller has a decision to make and needs the pieces of it.
// 409 because the request was well-formed and refused by the state of the
// storage, which is exactly what the wizard branches on.
func (e *ClaimConflictError) Detail() (int, any) {
	return 409, map[string]any{
		"error":        e.Error(),
		"kind":         "claim-conflict",
		"machine_name": e.MachineName,
		"target_name":  e.TargetName,
		"legacy":       e.Legacy,
	}
}

// locate records where a refused destination is, so the error can print the
// command that fixes it.
//
// Done by the caller rather than inside the marker check, which only ever holds
// an opened backend and genuinely does not know whether it is looking at a
// mount point or a share URL. Any other error passes straight through.
func locate(err error, kind, location string) error {
	var c *ClaimConflictError
	if errors.As(err, &c) {
		c.Kind, c.Location = kind, location
	}
	return err
}
