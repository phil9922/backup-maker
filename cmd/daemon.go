// SPDX-License-Identifier: MIT

package cmd

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/phil9922/backup-maker/internal/config"
	"github.com/phil9922/backup-maker/internal/daemon"
)

var daemonCmd = &cobra.Command{
	Use:   "daemon",
	Short: "Run the backup engine in the foreground (autostart runs this)",
	RunE: func(cmd *cobra.Command, args []string) error {
		if bg, _ := cmd.Flags().GetBool("background"); bg {
			return relaunchDetached()
		}
		cfg, err := config.Load()
		if err != nil {
			if os.IsNotExist(err) {
				cmd.SilenceUsage = true
				return errNotInitialized
			}
			return err
		}
		log := slog.New(slog.NewTextHandler(os.Stderr, nil))
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		return daemon.Run(ctx, cfg, log)
	},
}

func init() {
	daemonCmd.Flags().Bool("background", false, "detach and run in the background")
	rootCmd.AddCommand(daemonCmd)
}
