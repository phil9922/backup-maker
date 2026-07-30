// SPDX-License-Identifier: MIT

package cmd

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/phil9922/backup-maker/internal/config"
	"github.com/phil9922/backup-maker/internal/setup"
)

var renameTargetCmd = &cobra.Command{
	Use:   "rename-target <old-name> <new-name>",
	Short: "Change what a backup destination is called",
	Long: `Rename a destination. Nothing on the storage moves and no backup is
re-copied: the files live under <machine>/<folder-label>, and the marker on the
drive identifies it by a UUID rather than by this name. The destination does not
even have to be plugged in.

The name is what everything else points at, so this moves all of it in one go:
the destination itself, any snapshot schedule aimed at it, the stored password
for a network drive, the recorded identity of the storage, and every folder's
last-synced clock.

  backup-maker rename-target backups pi-drive1`,
	Args: cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		if _, err := config.Load(); err != nil {
			if os.IsNotExist(err) {
				return errNotInitialized
			}
			return err
		}
		from, to := args[0], args[1]
		if err := setup.RenameTarget(from, to); err != nil {
			return err
		}
		fmt.Printf("Renamed %q to %q. Nothing on the destination was moved.\n", from, to)
		return nil
	},
}

func init() {
	rootCmd.AddCommand(renameTargetCmd)
}
