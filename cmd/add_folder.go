// SPDX-License-Identifier: MIT

package cmd

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/phil9922/backup-maker/internal/config"
	"github.com/phil9922/backup-maker/internal/setup"
)

var addFolderCmd = &cobra.Command{
	Use:   "add-folder <path>",
	Short: "Start backing up a folder to every configured target",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if !config.Exists() {
			return errNotInitialized
		}
		label, _ := cmd.Flags().GetString("label")
		extraIgnore, _ := cmd.Flags().GetStringSlice("ignore")
		noDefaults, _ := cmd.Flags().GetBool("no-default-ignores")

		f, err := setup.AddFolder(args[0], label, extraIgnore, noDefaults)
		if err != nil {
			if os.IsNotExist(err) {
				return errNotInitialized
			}
			return err
		}
		fmt.Printf("Backing up %s (label %q, id %s)\n", f.Path, f.Label, f.ID)
		fmt.Println("The running daemon picks this up within seconds; check with: backup-maker status")
		return nil
	},
}

func init() {
	addFolderCmd.Flags().String("label", "", "display name (default: folder basename)")
	addFolderCmd.Flags().StringSlice("ignore", nil, "extra ignore patterns for this folder")
	addFolderCmd.Flags().Bool("no-default-ignores", false, "back up junk like node_modules too")
	rootCmd.AddCommand(addFolderCmd)
}
