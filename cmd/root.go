// SPDX-License-Identifier: MIT

// Package cmd implements the backup-maker CLI.
package cmd

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/phil9922/backup-maker/internal/config"
	"github.com/phil9922/backup-maker/internal/daemon"
)

var rootCmd = &cobra.Command{
	Use:   "backup-maker",
	Short: "Real-time, versioned, one-way backups to local drives and LAN machines",
	Long: `backup-maker keeps chosen folders continuously mirrored onto backup targets:
locally attached drives (SD cards, USB sticks, external disks) and other
machines on your LAN running backup-maker — any OS. Mirrors are one-way and
keep ~30 days of old file versions, so a mistake on the source can't destroy
the backup. Everything stays on your local network by default.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		if !config.Exists() {
			fmt.Println("No configuration yet. Run: backup-maker init")
			return nil
		}
		if c, err := daemon.Connect(); err == nil {
			_ = c // daemon already running
			return openDashboard()
		}
		fmt.Println("Daemon is not running. Start it with: backup-maker daemon")
		return nil
	},
	SilenceUsage:  true,
	SilenceErrors: true,
}

func Execute() {
	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, "Error:", err)
		os.Exit(1)
	}
}
