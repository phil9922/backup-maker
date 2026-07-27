// SPDX-License-Identifier: MIT

package cmd

import (
	"context"
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/phil9922/backup-maker/internal/discover"
)

var scanCmd = &cobra.Command{
	Use:   "scan",
	Short: "Look for network drives (SMB servers) on your local network",
	Long: `Sweeps your local network once for file servers — NAS boxes, routers with
USB drives, computers sharing folders. Runs only when you ask; backup-maker
never scans in the background.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		fmt.Println("Scanning your local network (a few seconds)…")
		hosts, err := discover.Scan(context.Background())
		if err != nil {
			return err
		}
		if len(hosts) == 0 {
			fmt.Println("No network drives found. If you expected one, check that it's powered on")
			fmt.Println("and that file sharing (SMB) is enabled on it. You can always add one by")
			fmt.Println("address: backup-maker add-target share //<host-or-ip>/<share>")
			return nil
		}
		for _, h := range hosts {
			switch {
			case len(h.Shares) > 0:
				fmt.Printf("%-20s %-15s  shares: %s\n", h.Name, h.Addr, strings.Join(h.Shares, ", "))
			case h.NeedsAuth:
				fmt.Printf("%-20s %-15s  (locked — needs credentials)\n", h.Name, h.Addr)
			default:
				fmt.Printf("%-20s %-15s  (no shares visible)\n", h.Name, h.Addr)
			}
		}
		fmt.Println()
		fmt.Println("Add one as a backup target:")
		fmt.Println("  backup-maker add-target share //<name-or-ip>/<share>            # open/guest shares")
		fmt.Println("  backup-maker add-target share //<name-or-ip>/<share> --user bob # locked shares")
		return nil
	},
}

func init() {
	rootCmd.AddCommand(scanCmd)
}
