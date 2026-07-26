// SPDX-License-Identifier: MIT

package cmd

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/phil9922/backup-maker/internal/daemon"
	"github.com/phil9922/backup-maker/internal/status"
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
		fmt.Printf("Network: local only   Machine-sync engine: %s\n\n", engine)

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
		}
		for _, p := range m.PendingSources {
			fmt.Printf("\n!! Machine %q (%s) wants to back up here.\n", p.Name, p.DeviceID)
			fmt.Println("   Approve with: backup-maker pair accept " + p.DeviceID)
		}
		return nil
	},
}

func humanBytes(n int64) string {
	switch {
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
