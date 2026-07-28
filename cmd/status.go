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
			tw := newTable("FOLDER", "TARGET", "STATE", "PENDING", "LAST SEEN/SYNC")
			for _, r := range m.Rows {
				pending := "-"
				if r.NeedItems > 0 {
					pending = fmt.Sprintf("%d (%s)", r.NeedItems, humanBytes(r.NeedBytes))
				}
				mark := ""
				if r.Stale {
					mark = "  !!"
				}
				tw.add(r.FolderLabel, r.TargetName, r.State+mark, pending, humanTime(r.LastSeen))
			}
			tw.print()
		}

		if len(m.Archives) > 0 {
			fmt.Println()
			tw := newTable("SCHEDULED BACKUP", "TARGET", "EVERY", "STATE", "LAST RUN")
			for _, a := range m.Archives {
				mark := ""
				if a.State == "failed" || a.State == "due" {
					mark = "  !!"
				}
				tw.add(a.Name, a.Target, a.Every, a.State+mark, humanTime(a.LastRun))
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
