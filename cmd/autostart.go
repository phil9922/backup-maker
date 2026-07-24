// SPDX-License-Identifier: MIT

package cmd

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/phil9922/backup-maker/internal/autostart"
)

var autostartCmd = &cobra.Command{
	Use:   "autostart enable|disable|status",
	Short: "Start backup-maker automatically when you log in",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		switch args[0] {
		case "enable":
			if err := autostart.Enable(); err != nil {
				return err
			}
			fmt.Println("Autostart enabled — backups now run whenever you're logged in.")
			return nil
		case "disable":
			if err := autostart.Disable(); err != nil {
				return err
			}
			fmt.Println("Autostart disabled.")
			return nil
		case "status":
			s, err := autostart.Status()
			if err != nil {
				return err
			}
			fmt.Println(s)
			return nil
		default:
			return fmt.Errorf("unknown subcommand %q (use enable, disable, or status)", args[0])
		}
	},
}

func init() {
	rootCmd.AddCommand(autostartCmd)
}
