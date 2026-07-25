// SPDX-License-Identifier: MIT

package cmd

import (
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/phil9922/backup-maker/internal/config"
	"github.com/phil9922/backup-maker/internal/pairing"
	"github.com/phil9922/backup-maker/internal/syncthing"
)

// engineClient reaches our supervised syncthing directly (the daemon must be
// running for its child to exist).
func engineClient() (*syncthing.Client, error) {
	state, err := config.LoadState()
	if err != nil {
		return nil, err
	}
	if state.SyncthingAPIKey == "" || state.SyncthingGUIPort == 0 {
		return nil, fmt.Errorf("the machine-sync engine hasn't started yet — it runs only once machine sync is configured.\nWith the daemon running, either accept backups here (backup-maker receive enable --root <path>)\nor add a machine target (backup-maker add-target device <id>), then retry")
	}
	c := syncthing.NewClient(state.SyncthingGUIPort, state.SyncthingAPIKey)
	if err := c.Ping(); err != nil {
		return nil, fmt.Errorf("machine-sync engine not running — is the daemon running, and is a machine target or receiving configured?")
	}
	return c, nil
}

var pairCmd = &cobra.Command{
	Use:   "pair",
	Short: "Show this machine's device ID for pairing",
	RunE: func(cmd *cobra.Command, args []string) error {
		c, err := engineClient()
		if err != nil {
			return err
		}
		id, err := c.MyID()
		if err != nil {
			return err
		}
		fmt.Println("This machine's device ID:")
		fmt.Println("  " + id)
		fmt.Println()
		fmt.Println("On the machine you want to back up FROM, run:")
		fmt.Println("  backup-maker add-target device " + id)
		return nil
	},
}

var pairPendingCmd = &cobra.Command{
	Use:   "pending",
	Short: "List machines waiting for your approval",
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := config.Load()
		if err != nil {
			if os.IsNotExist(err) {
				return errNotInitialized
			}
			return err
		}
		c, err := engineClient()
		if err != nil {
			return err
		}
		pend, err := pairing.PendingSources(c, cfg)
		if err != nil {
			return err
		}
		if len(pend) == 0 {
			fmt.Println("No machines waiting for approval.")
			return nil
		}
		for _, p := range pend {
			name := p.Name
			if name == "" {
				name = "(unnamed)"
			}
			fmt.Printf("%s  %s  seen at %s\n", p.DeviceID, name, p.Address)
		}
		fmt.Println("\nApprove one with: backup-maker pair accept <DEVICE-ID>")
		fmt.Println("Compare the ID with what \"backup-maker pair\" prints on that machine before approving.")
		return nil
	},
}

var pairAcceptCmd = &cobra.Command{
	Use:   "accept <DEVICE-ID>",
	Short: "Approve a machine so its backups are accepted here",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := config.Load()
		if err != nil {
			if os.IsNotExist(err) {
				return errNotInitialized
			}
			return err
		}
		if !cfg.Receive.Enabled {
			return fmt.Errorf("enable receiving first: backup-maker receive enable --root <path>")
		}
		id := strings.ToUpper(strings.TrimSpace(args[0]))

		// Allow approving by unique prefix of a pending device's ID.
		if len(id) < 50 {
			c, err := engineClient()
			if err != nil {
				return err
			}
			pend, err := pairing.PendingSources(c, cfg)
			if err != nil {
				return err
			}
			var matches []string
			for _, p := range pend {
				if strings.HasPrefix(p.DeviceID, strings.TrimSuffix(id, "-…")) {
					matches = append(matches, p.DeviceID)
				}
			}
			switch len(matches) {
			case 1:
				id = matches[0]
			case 0:
				return fmt.Errorf("no pending machine matches %q (see: backup-maker pair pending)", id)
			default:
				return fmt.Errorf("%q matches several pending machines; paste the full ID", id)
			}
		}

		for _, s := range cfg.Receive.AcceptedSources {
			if s == id {
				fmt.Println("Already approved.")
				return nil
			}
		}
		cfg.Receive.AcceptedSources = append(cfg.Receive.AcceptedSources, id)
		if err := cfg.Save(); err != nil {
			return err
		}
		fmt.Println("Approved. Its folders will start arriving shortly; watch with: backup-maker status")
		return nil
	},
}

func init() {
	pairCmd.AddCommand(pairPendingCmd, pairAcceptCmd)
	rootCmd.AddCommand(pairCmd)
}
