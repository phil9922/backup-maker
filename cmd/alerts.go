// SPDX-License-Identifier: MIT

package cmd

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/phil9922/backup-maker/internal/daemon"
	"github.com/phil9922/backup-maker/internal/status"
)

var alertsAll bool

var alertsCmd = &cobra.Command{
	Use:   "alerts",
	Short: "What backup-maker has told you, and whether it got out",
	Long: `List the alerts this machine has raised, newest first.

Alerts fire when something changes — a destination stops being reachable, a
snapshot fails, either of them recovers — and they are delivered to whatever is
switched on: a desktop notification, a webhook, your phone. All of those
disappear once read, and a machine with no desktop never showed one at all. This
is the record.

Each line says where the alert got to. "delivered nowhere" is the one to look
for: it means the alert was raised and nothing carried it, which is the state
where backups can fail without you ever hearing about it.`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		c, err := daemon.Connect()
		if err != nil {
			return err
		}
		var m status.Model
		if err := c.Status(&m); err != nil {
			return err
		}
		if len(m.RecentAlerts) == 0 {
			fmt.Println("Nothing has been raised on this machine.")
			fmt.Println("Alerts fire when something changes, so an empty list is the good outcome.")
			return nil
		}

		show := m.RecentAlerts
		const brief = 10
		if !alertsAll && len(show) > brief {
			show = show[:brief]
		}
		for _, a := range show {
			mark := "  "
			if a.Urgent {
				mark = "!!"
			}
			fmt.Printf("%s %-12s %s\n", mark, humanTime(a.At), a.Title)
			if a.Body != "" {
				fmt.Printf("               %s\n", a.Body)
			}
			fmt.Printf("               %s, %s\n", a.At.Format("2 Jan 2006, 15:04"), deliveryLine(a.Delivered, a.Failed))
		}
		if len(show) < len(m.RecentAlerts) {
			fmt.Printf("\n%d older, shown with: backup-maker alerts --all\n", len(m.RecentAlerts)-len(show))
		}
		return nil
	},
}

// deliveryLine says where one alert actually got to.
//
// The empty case is the point of the whole column: an alert with no method at
// all was raised into silence, which is invisible everywhere else in the
// program and is the normal state of a machine nobody has set delivery up on.
func deliveryLine(delivered, failed []string) string {
	switch {
	case len(delivered) == 0 && len(failed) == 0:
		return "delivered nowhere — no delivery method was switched on"
	case len(failed) == 0:
		return "delivered by " + strings.Join(delivered, ", ")
	case len(delivered) == 0:
		return "DELIVERED NOWHERE — " + strings.Join(failed, ", ") + " failed"
	default:
		return "delivered by " + strings.Join(delivered, ", ") +
			"; " + strings.Join(failed, ", ") + " failed"
	}
}

func init() {
	alertsCmd.Flags().BoolVar(&alertsAll, "all", false, "show every alert kept, not just the most recent")
	rootCmd.AddCommand(alertsCmd)
}
