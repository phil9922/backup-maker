// SPDX-License-Identifier: MIT

package cmd

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/phil9922/backup-maker/internal/config"
)

var receiveCmd = &cobra.Command{
	Use:   "receive",
	Short: "Let other machines back up to this computer",
}

var receiveEnableCmd = &cobra.Command{
	Use:   "enable --root <path>",
	Short: "Accept backups from approved machines into a folder here",
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := config.Load()
		if err != nil {
			if os.IsNotExist(err) {
				return errNotInitialized
			}
			return err
		}
		root, _ := cmd.Flags().GetString("root")
		if root == "" {
			return fmt.Errorf("--root is required, e.g. --root /mnt/backups or --root D:\\Backups")
		}
		root, err = filepath.Abs(root)
		if err != nil {
			return err
		}
		if err := os.MkdirAll(root, 0o755); err != nil {
			return fmt.Errorf("cannot create %s: %w", root, err)
		}
		cfg.Receive.Enabled = true
		cfg.Receive.Root = root
		if err := cfg.Save(); err != nil {
			return err
		}
		fmt.Printf("Receiving backups into %s\n", root)
		fmt.Println("Each source machine still needs approval: backup-maker pair accept <DEVICE-ID>")
		fmt.Println("Show this machine's ID to the source with: backup-maker pair")
		return nil
	},
}

var receiveDisableCmd = &cobra.Command{
	Use:   "disable",
	Short: "Stop accepting backups (existing files stay put)",
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := config.Load()
		if err != nil {
			return err
		}
		cfg.Receive.Enabled = false
		if err := cfg.Save(); err != nil {
			return err
		}
		fmt.Println("No longer accepting backups. Files already received remain on disk.")
		return nil
	},
}

func init() {
	receiveEnableCmd.Flags().String("root", "", "directory where backups from other machines land")
	receiveCmd.AddCommand(receiveEnableCmd, receiveDisableCmd)
	rootCmd.AddCommand(receiveCmd)
}
