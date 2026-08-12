// SPDX-License-Identifier: MIT

package status

// The words every surface uses for a state, and the colour that goes with them.
//
// WHY THIS IS ONE PLACE. The same translation was written out separately for the
// CLI and for the status page left on each destination, and they drifted twice.
// Once over "scheduled" versus "timed", fixed in v0.1.9. Then again over this
// very mapping: the dashboard and `backup-maker status` were taught to say
// "backed up", and the status page beside the backups went on saying "in sync"
// for two releases — so the one surface you read when the computer is off used
// vocabulary the documentation no longer contained.
//
// A copy of a mapping is a copy that will be forgotten. Anything rendering a
// state calls these.
//
// THE DASHBOARD IS STILL SEPARATE, and cannot be otherwise: it is JavaScript.
// `stateLabel`/`stateClass` in app.js must be changed in step with this file, and
// TestEveryStateHasTheSameWordsInTheBrowser reads app.js to check they agree.
//
// WHAT "paused" OWES THE DASHBOARD, recorded here because this is where the
// mapping lives and app.js has to be brought along by hand:
//
//   - stateClass must return 'paused' (drawn muted) for it. Everything that
//     falls off the end of that function is 'bad', and a deliberate stop drawn
//     red sends somebody hunting a fault nobody has.
//   - renderVerdict must not reach "Everything is backed up" while any row
//     carries paused:true. It derives faults from stateClass, and muting the
//     state — correctly — takes a paused destination out of `broken`, so the
//     headline needs a branch of its own from `rows.filter(r => r.paused)`,
//     after the broken/unprotected branches and before "Backing up now":
//     "N backups are paused; everything else is backed up", drawn muted. A
//     headline claiming everything is backed up above a row that is not being
//     copied to is the one line this page exists to get right.

// RowLabel answers the only question this column is ever read for: are the files
// safe.
//
// "scanning" and "syncing" answer a different one — what the program is doing —
// and a destination holding a complete copy showed "scanning" for minutes at a
// stretch, leaving the honest reading as "I cannot tell whether I have a backup".
// That is the worst sentence a backup tool can produce, and it was producing it
// while everything was fine. The activity belongs in a progress column instead.
//
// A destination that has never finished a pass has no copy to promise, and says
// that rather than borrowing the reassurance.
func RowLabel(r Row) string {
	switch r.State {
	case "scanning", "syncing":
		if r.FirstBackup {
			return "first backup"
		}
		return "backed up"
	case "in sync":
		return "backed up"
	case "paused":
		// SAID OUT LOUD RATHER THAN LEFT TO THE FALLTHROUGH BELOW, which exists
		// to stop faults being softened — and this is not a fault. A paused
		// mirror is also the one state here that must never borrow "backed up":
		// what is on the destination stays there, but nothing saved since is
		// going anywhere, and this column answers "are my files safe", not "was
		// there ever a copy". The same word the snapshot side uses for the same
		// deliberate stop.
		return "paused"
	}
	// EVERYTHING ELSE KEEPS ITS OWN NAME, and that is the other half of the rule:
	// reframing must not swallow the states that mean something is wrong. Only
	// the safety question is answered here — a fault still reads as a fault.
	//
	// The dashboard does soften two of them ("waiting to pair", "name already in
	// use") because it can put an explanation of the remedy beside them in a
	// tooltip. A column in a terminal cannot, so a friendlier word there would
	// cost the reader the term they need to search for. Deliberately not shared.
	return r.State
}

// RowHealth is the colour that belongs beside RowLabel: "ok", "busy", "bad" or
// "muted".
//
// GREEN ONCE THERE IS A COPY, including while backup-maker looks for changes —
// amber beside the word "backed up" takes the reassurance straight back, and the
// dot has to answer the same question the word does. Amber is for a first backup,
// which really is still in progress.
//
// Things that are not faults must not be red. A machine waiting to be paired is
// waiting on a person; a folder with nowhere to send copies yet is waiting on a
// decision. Red sends somebody hunting a fault that does not exist.
func RowHealth(r Row) string {
	switch r.State {
	case "in sync":
		return "ok"
	case "scanning", "syncing":
		if r.FirstBackup {
			return "busy"
		}
		return "ok"
	case "awaiting-pair":
		return "busy"
	case "no destination yet":
		return "muted"
	case "no folders assigned":
		// The mirror-side counterpart of "no destination yet", and not a fault
		// either. rollUp gives this to a target with no rows, which is the
		// permanent and correct state of an `archives_only` destination —
		// FoldersForTarget gives those no folders on purpose. Red here reports a
		// broken destination whose snapshots are arriving on schedule, and the
		// dashboard's verdict counts every "bad" target, so it also puts "needs
		// attention" at the top of a healthy page.
		return "muted"
	case "paused":
		// A PAUSED MIRROR IS NEITHER WORKING NOR BROKEN, so it gets neither
		// colour — the same reading ArchiveHealth gives a paused schedule, for
		// the same reason. Green would say this destination is up to date and
		// red would send somebody hunting a fault that nobody has. Muted says
		// what is true: somebody switched this off, and it is waiting for them.
		//
		// AND IT MUST BE SAID HERE. Everything that falls off the end of this
		// switch is red, which is how "no folders assigned" once reddened every
		// healthy destination on the machine and put "needs attention" at the top
		// of a page with nothing wrong on it.
		return "muted"
	}
	return "bad" // offline, stale, full, wrong-drive, name-clash, error
}

// ArchiveLabel answers for a snapshot schedule what RowLabel answers for a
// mirror, in the same words. The two tables sit on one page describing two kinds
// of backup, and there is no reason a snapshot should report "ok" where a mirror
// reports "backed up".
//
// "ok" and "due" both mean a snapshot exists and was written successfully — a job
// being due says the NEXT one is owed, not that the last one is missing. A job
// packing its very first zip has nothing on the destination yet and must not
// claim otherwise; one packing its second has last time's zip sitting there the
// whole time, so it is backed up while it works.
func ArchiveLabel(a ArchiveRow) string {
	switch a.State {
	case "ok", "due":
		return "backed up"
	case "running", "preparing":
		if a.LastRun.IsZero() {
			return "first backup"
		}
		return "backed up"
	case "never run":
		// No zip exists. Naming it plainly, rather than "never run", says what it
		// means for the person's files rather than for the schedule.
		return "not backed up yet"
	}
	return a.State // failed, needs password, paused: faults and deliberate stops
}

// ArchiveHealth is the colour beside ArchiveLabel.
//
// A PAUSED SCHEDULE IS NEITHER WORKING NOR BROKEN, so it gets neither colour:
// green would imply it is about to run and red that something went wrong. And
// anything working, or about to, is busy rather than bad — the status page used
// to paint everything that was not literally "ok" red, which made a schedule that
// was merely *due* look like a failure on the one page you read when the computer
// it describes is switched off.
func ArchiveHealth(a ArchiveRow) string {
	if a.Paused {
		return "muted"
	}
	switch a.State {
	case "ok", "due":
		return "ok"
	case "running", "preparing":
		if a.LastRun.IsZero() {
			return "busy"
		}
		return "ok"
	case "never run":
		return "busy"
	}
	if a.NeedsPassword {
		return "busy" // waiting on a person, not broken
	}
	return "bad"
}
