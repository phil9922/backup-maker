// SPDX-License-Identifier: MIT

package cmd

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/phil9922/backup-maker/internal/config"
	"github.com/phil9922/backup-maker/internal/smbfs"
)

var setPasswordCmd = &cobra.Command{
	Use:   "set-password <target-name>",
	Short: "Update the stored credentials for a network-drive target",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := config.Load()
		if err != nil {
			if os.IsNotExist(err) {
				return errNotInitialized
			}
			return err
		}
		name := args[0]
		var target *config.Target
		for i := range cfg.Targets {
			if cfg.Targets[i].Name == name && cfg.Targets[i].Type == "share" {
				target = &cfg.Targets[i]
				break
			}
		}
		if target == nil {
			return fmt.Errorf("no network-drive target named %q (see: backup-maker status)", name)
		}

		if user, _ := cmd.Flags().GetString("user"); user != "" {
			target.Username = user
		}
		pass, err := promptPassword(fmt.Sprintf("Password for %s on %s: ", target.Username, target.URL))
		if err != nil {
			return err
		}

		fmt.Println("Testing connection…")
		if err := smbfs.TestConnection(target.URL, target.Username, pass); err != nil {
			return err
		}

		state, err := config.LoadState()
		if err != nil {
			return err
		}
		if state.ShareCredentials == nil {
			state.ShareCredentials = map[string]string{}
		}
		state.ShareCredentials[name] = pass
		if err := state.Save(); err != nil {
			return err
		}
		// Re-save the config (even if unchanged) so a running daemon reloads
		// and picks up the new credentials.
		if err := cfg.Save(); err != nil {
			return err
		}
		fmt.Println("Credentials updated.")
		return nil
	},
}

func init() {
	setPasswordCmd.Flags().String("user", "", "also change the username")
	rootCmd.AddCommand(setPasswordCmd)
}
