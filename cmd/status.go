// SPDX-License-Identifier: MIT

package cmd

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/phil9922/backup-maker/internal/daemon"
	"github.com/phil9922/backup-maker/internal/status"
	"github.com/phil9922/backup-maker/internal/version"
)

var errNotInitialized = errors.New("no configuration found — run: backup-maker init")

var statusCmd = &cobra.Command{
	Use:   "status",
	Short: "Show backup health for every folder and target",
	RunE: func(cmd *cobra.Command, args []string) error {
		c, err := daemon.Connect()
		if err != nil {
			return err
		}
		var m status.Model
		if err := c.Status(&m); err != nil {
			return err
		}

		engine := "running"
		if !m.EngineOK {
			if m.EngineNeeded {
				engine = "NOT RUNNING"
			} else {
				engine = "off (no machine targets configured)"
			}
		}
		if m.DeviceID != "" {
			fmt.Printf("Machine: %s   Device ID: %s\n", m.MachineName, m.DeviceID)
		} else {
			fmt.Printf("Machine: %s\n", m.MachineName)
		}
		fmt.Printf("Network: local only   Machine-sync engine: %s\n", engine)
		if line := totalsLine(m.Totals); line != "" {
			fmt.Println(line)
		}
		fmt.Println()

		if len(m.Rows) == 0 {
			fmt.Println("Nothing is being backed up yet.")
			fmt.Println("  backup-maker add-folder <path>          # choose what to protect")
			fmt.Println("  backup-maker add-target drive <mount>   # a local SD/USB/disk")
			fmt.Println("  backup-maker add-target device <id>     # another machine on your LAN")
		} else {
			tw := newTable("FOLDER", "TARGET", "STATE", "PROGRESS", "LAST SEEN/SYNC")
			for _, r := range m.Rows {
				mark := ""
				if r.Stale {
					mark = "  !!"
				}
				tw.add(r.FolderLabel, r.TargetName, rowState(r)+mark, rowProgress(r), rowLastSeen(r))
			}
			tw.print()
		}

		// A folder nothing backs up has no target, so it produces no row above
		// and no schedule below — it is absent from every table on this page.
		// That is precisely how one stayed unnoticed, so it gets said in words.
		for _, f := range m.Folders {
			// nil means the running daemon predates this field and has not
			// answered — not that the folder is unprotected. Warning on silence
			// would flag every folder on the machine during an upgrade.
			if f.Protected == nil || *f.Protected {
				continue
			}
			fmt.Printf("\n!! %s is not backed up by anything.\n", f.Label)
			fmt.Printf("   %s is still listed here, but no destination mirrors it and no schedule snapshots it.\n", f.Path)
			fmt.Println("   Give it a backup with:  backup-maker wizard")
			fmt.Printf("   Or stop listing it in the dashboard's \"Not backed up by anything\" section.\n")
		}

		if len(m.Archives) > 0 {
			fmt.Println()
			tw := newTable("SCHEDULED BACKUP", "TARGET", "EVERY", "STATE", "LAST RUN")
			for _, a := range m.Archives {
				mark := ""
				if a.State == "failed" || a.State == "due" {
					mark = "  !!"
				}
				tw.add(a.Name, a.Target, a.Every, archiveState(a)+mark, humanTime(a.LastRun))
			}
			tw.print()
			for _, a := range m.Archives {
				if a.Detail != "" {
					fmt.Printf("  %s: %s\n", a.Name, a.Detail)
				}
			}
		}

		if m.Receive.Enabled {
			fmt.Printf("\nReceiving backups into %s\n", m.Receive.Root)
			// Drift on a received backup is silent by nature — nothing here
			// syncs it back — so the headline health command has to mention it.
			for _, f := range m.ReceivedFolders {
				if f.ChangedItems > 0 {
					fmt.Printf("!! %s: %d item(s) were changed on THIS machine and no longer match %s\n",
						f.Label, f.ChangedItems, sourceName(f.Source))
					fmt.Println("   Undo them with: backup-maker receive revert " + f.ID)
				}
			}
		}
		for _, p := range m.PendingSources {
			fmt.Printf("\n!! Machine %q (%s) wants to back up here.\n", p.Name, p.DeviceID)
			fmt.Println("   Approve with: backup-maker pair accept " + p.DeviceID)
		}
		// Advisory, and phrased so it cannot be mistaken for a fault: a newer
		// release existing says nothing about whether backups are working, and
		// this command's job is to answer that question first.
		if m.Settings.UpdateAvailable != "" {
			fmt.Printf("\nbackup-maker %s is available (you have %s). Your backups are fine.\n",
				strings.TrimPrefix(m.Settings.UpdateAvailable, "v"), version.Short())
			fmt.Println("   Download it from: https://github.com/phil9922/backup-maker/releases/latest")
		}
		// Said by the health command rather than only at install time, because
		// the failure it describes has no other symptom: the daemon runs, the
		// dashboard is green, and the recovery machinery is switched off.
		if w := autostartWarning(); w != "" {
			fmt.Print("\n" + w)
		}
		return nil
	},
}

// sourceName renders the machine a received backup came from, for the cases
// where the engine could not tell us which device it was.
func sourceName(source string) string {
	if source == "" {
		return "the machine that sent it"
	}
	return source
}

// totalsLine renders the lifetime odometer as one plain sentence for the status
// header. An empty result means there is nothing honest to say — no
// destinations are configured at all — and the caller prints no line.
//
// The three cases exist because a bare number would misinform in two of them:
// on a machine whose only destinations are paired computers nothing passes
// through our copy loop, so "0B" would read as "you have never been backed up"
// when the truth is "this counter does not cover your setup"; and where both
// kinds are configured the figure is real but partial, so it says which half it
// counts rather than quietly implying it counts everything.
func totalsLine(t status.Totals) string {
	if !t.Counted() {
		if t.DeviceTargets > 0 {
			return "Backed up in total: not counted on this machine — every destination here is another computer, and the sync engine transfers those itself."
		}
		return ""
	}
	partial := ""
	if t.Partial() {
		partial = " (drives and shares only; copies sent to a paired computer are transferred by the sync engine and are not counted)"
	}
	if t.Files == 0 {
		return "Backed up in total: nothing copied yet" + partial
	}
	line := "Backed up in total: " + humanBytes(int64(t.Bytes)) + " across " + humanFiles(t.Files)
	if !t.Since.IsZero() {
		line += " since " + t.Since.Format("2 January 2006")
	}
	return line + partial
}

// humanFiles renders a file count with thousands separators, singular where it
// belongs: "1 file", "82,391 files".
func humanFiles(n uint64) string {
	digits := strconv.FormatUint(n, 10)
	var sb strings.Builder
	for i, d := range digits {
		if i > 0 && (len(digits)-i)%3 == 0 {
			sb.WriteByte(',')
		}
		sb.WriteRune(d)
	}
	if n == 1 {
		return sb.String() + " file"
	}
	return sb.String() + " files"
}

func humanBytes(n int64) string {
	switch {
	case n >= 1<<40:
		return fmt.Sprintf("%.1fTB", float64(n)/(1<<40))
	case n >= 1<<30:
		return fmt.Sprintf("%.1fGB", float64(n)/(1<<30))
	case n >= 1<<20:
		return fmt.Sprintf("%.1fMB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.1fKB", float64(n)/(1<<10))
	default:
		return fmt.Sprintf("%dB", n)
	}
}

// rowState answers the only question this column is ever read for: are the
// files safe.
//
// "scanning" and "syncing" answer a different one — what the program is doing —
// and a destination holding a complete copy showed "scanning" for minutes at a
// stretch, leaving the honest reading as "I cannot tell whether I have a
// backup". That is the worst sentence a backup tool can produce, and it was
// producing it while everything was fine. The activity moved to the PROGRESS
// column, where it belongs.
func rowState(r status.Row) string {
	switch r.State {
	case "scanning", "syncing":
		if r.FirstBackup {
			return "first backup"
		}
		return "backed up"
	case "in sync":
		return "backed up"
	}
	return r.State
}

// archiveState answers the same question rowState answers for a mirror: are
// the files safe. It uses the same words, because the two tables sit on one
// page describing two kinds of backup, and there is no reason a snapshot should
// report "ok" where a mirror reports "backed up".
//
// "ok" and "due" both mean a snapshot exists and was written successfully — a
// job being due says the NEXT one is owed, not that the last one is missing.
// A job packing its very first zip has nothing on the destination yet and must
// not claim otherwise; one packing its second has last time's zip sitting there
// the whole time, so it is backed up while it works.
func archiveState(a status.ArchiveRow) string {
	switch a.State {
	case "ok", "due":
		return "backed up"
	case "running", "preparing":
		if a.LastRun.IsZero() {
			return "first backup"
		}
		return "backed up"
	case "never run":
		// No zip exists. Naming it plainly, rather than "never run", says what
		// it means for the person's files rather than for the schedule.
		return "not backed up yet"
	}
	return a.State // failed, needs password, paused: faults and deliberate stops
}

// rowProgress says what a destination is doing right now, in the unit that
// matters for the phase it is in.
//
// The column used to print "-" for everything that was not mid-transfer, which
// includes the whole scanning phase — the part that takes the minutes on a
// network share. Between that and "never" in the next column, a destination
// steadily working through 70,000 files displayed as three dashes and a word.
func rowProgress(r status.Row) string {
	if r.State == "scanning" {
		// One sentence for the whole scan, because from outside it is one
		// activity: looking for what changed. The stage only decides which
		// counter is honest to show against it.
		what := "checking for changes"
		if r.FirstBackup {
			what = "working out what to copy"
		}
		if r.Phase == "tidying" {
			what = "checking for deleted files"
		}
		if r.Phase == "source" && r.ScannedFiles == 0 {
			return what
		}
		if r.ScanTotal > 0 {
			return fmt.Sprintf("%s: %s of %s", what, humanCount(r.ScannedFiles), humanCount(r.ScanTotal))
		}
		if r.ScannedFiles > 0 {
			return fmt.Sprintf("%s: %s so far", what, humanCount(r.ScannedFiles))
		}
		return what
	}
	if r.NeedItems > 0 {
		return fmt.Sprintf("%d left (%s)", r.NeedItems, humanBytes(r.NeedBytes))
	}
	return "-"
}

// rowLastSeen never says "never" about a destination that is working. A first
// pass takes minutes, and until one completes there is no timestamp to print —
// but "never" describes a destination nothing has been written to, which is a
// different and much worse thing than one that has not finished yet.
func rowLastSeen(r status.Row) string {
	if r.FirstBackup {
		return "first backup running"
	}
	return humanTime(r.LastSeen)
}

// humanCount groups thousands, because six digits of file count are unreadable
// without it.
func humanCount(n int64) string {
	s := strconv.FormatInt(n, 10)
	if len(s) <= 3 {
		return s
	}
	var b strings.Builder
	for i, c := range s {
		if i > 0 && (len(s)-i)%3 == 0 {
			b.WriteByte(',')
		}
		b.WriteRune(c)
	}
	return b.String()
}

func humanTime(t time.Time) string {
	if t.IsZero() {
		return "never"
	}
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds ago", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	}
}

// tiny fixed-width table writer
type table struct {
	headers []string
	rows    [][]string
}

func newTable(headers ...string) *table { return &table{headers: headers} }

func (t *table) add(cells ...string) { t.rows = append(t.rows, cells) }

func (t *table) print() {
	widths := make([]int, len(t.headers))
	for i, h := range t.headers {
		widths[i] = len(h)
	}
	for _, r := range t.rows {
		for i, c := range r {
			if len(c) > widths[i] {
				widths[i] = len(c)
			}
		}
	}
	printRow := func(cells []string) {
		var sb strings.Builder
		for i, c := range cells {
			sb.WriteString(c)
			if i < len(cells)-1 {
				sb.WriteString(strings.Repeat(" ", widths[i]-len(c)+2))
			}
		}
		fmt.Println(sb.String())
	}
	printRow(t.headers)
	for _, r := range t.rows {
		printRow(r)
	}
}

func init() {
	rootCmd.AddCommand(statusCmd)
}
