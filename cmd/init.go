// SPDX-License-Identifier: MIT

package cmd

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/phil9922/backup-maker/internal/config"
)

var initCmd = &cobra.Command{
	Use:   "init",
	Short: "Create the initial configuration for this machine",
	RunE: func(cmd *cobra.Command, args []string) error {
		if config.Exists() {
			path, _ := config.ConfigPath()
			return fmt.Errorf("config already exists at %s", path)
		}
		cfg := config.New()
		if name, _ := cmd.Flags().GetString("name"); name != "" {
			cfg.General.MachineName = name
		}
		if err := cfg.Save(); err != nil {
			return err
		}
		path, _ := config.ConfigPath()
		fmt.Printf("Created %s (machine name: %s)\n", path, cfg.General.MachineName)
		fmt.Println("Next: backup-maker daemon   # start the engine")
		maybeOfferAdvisor()
		return nil
	},
}

func init() {
	initCmd.Flags().String("name", "", "machine name shown to backup targets (default: hostname)")
	rootCmd.AddCommand(initCmd)
}
