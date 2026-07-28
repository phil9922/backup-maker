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
			if w := autostartWarning(); w != "" {
				fmt.Print(w)
			}
			return nil
		default:
			return fmt.Errorf("unknown subcommand %q (use enable, disable, or status)", args[0])
		}
	},
}

// autostartWarning is the message shown when the installed service definition
// is older than the one this binary would write, and "" when there is nothing
// to say.
//
// THE FAILURE IT EXISTS FOR. Restart policy and the watchdog live in the
// service definition, and only `autostart enable` ever rewrites it. Upgrading
// the binary therefore leaves the new protections on disk and inert, with no
// symptom at all until the day the daemon wedges and nothing restarts it.
//
// Any error is swallowed: this is an advisory on the end of another command's
// output, and a machine where the check itself cannot run is not a machine that
// should be told its backups are misconfigured.
func autostartWarning() string {
	r, err := autostart.Check()
	if err != nil || !r.NeedsReinstall() {
		return ""
	}
	return "!! The installed service definition is out of date — it was written by an\n" +
		"   older version of backup-maker, and the protections this version relies on\n" +
		"   (restart policy, wedged-daemon watchdog) are not in effect.\n" +
		"   Fix with: backup-maker autostart enable\n" +
		"   File: " + r.Path + "\n"
}

func init() {
	rootCmd.AddCommand(autostartCmd)
}
