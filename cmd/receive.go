// SPDX-License-Identifier: MIT

package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/phil9922/backup-maker/internal/config"
	"github.com/phil9922/backup-maker/internal/daemon"
	"github.com/phil9922/backup-maker/internal/status"
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

var receiveStatusCmd = &cobra.Command{
	Use:   "status",
	Short: "List the backups other machines keep here, and any local changes to them",
	RunE: func(cmd *cobra.Command, args []string) error {
		c, err := daemon.Connect()
		if err != nil {
			return err
		}
		var m status.Model
		if err := c.Status(&m); err != nil {
			return err
		}
		if !m.Receive.Enabled {
			fmt.Println("This machine is not receiving backups.")
			fmt.Println("Turn it on with: backup-maker receive enable --root <path>")
			return nil
		}
		fmt.Printf("Receiving backups into %s\n\n", m.Receive.Root)
		if len(m.ReceivedFolders) == 0 {
			fmt.Println("No backups have arrived yet.")
			fmt.Println("Machines waiting for approval: backup-maker pair pending")
			return nil
		}
		tw := newTable("FOLDER", "FROM", "ID", "CHANGED HERE")
		drifted := 0
		for _, f := range m.ReceivedFolders {
			changed := "-"
			if f.ChangedItems > 0 {
				drifted++
				changed = fmt.Sprintf("%d item(s), %s", f.ChangedItems, humanBytes(f.ChangedBytes))
			}
			tw.add(f.Label, sourceName(f.Source), f.ID, changed)
		}
		tw.print()
		// Drift means this copy is no longer what the other machine holds, which
		// is the whole reason to look at this table.
		if drifted > 0 {
			fmt.Println("\n!! Files in the folders marked above were changed on THIS machine,")
			fmt.Println("   so they no longer match the machine that sent them.")
			fmt.Println("   Put a folder back the way its sender has it with:")
			fmt.Println("     backup-maker receive revert <ID>")
		}
		return nil
	},
}

var receiveRevertCmd = &cobra.Command{
	Use:   "revert <FOLDER-ID>",
	Short: "Discard changes made here to a received backup, restoring the sender's copy",
	Long: "Restore one backup held on this machine to exactly what the machine that sent it has.\n\n" +
		"This DELETES files that were added here and UNDOES edits made here; files\n" +
		"deleted here come back. Anything that exists only on this machine is lost.\n" +
		"List what is held here with: backup-maker receive status",
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		id := strings.TrimSpace(args[0])
		yes, _ := cmd.Flags().GetBool("yes")
		if !yes {
			// The warning is printed whether or not --yes was given, so nobody
			// learns what revert does only after it has run.
			fmt.Printf("Reverting %q will:\n", id)
			printRevertWarning()
			return fmt.Errorf("not reverting without confirmation; re-run with --yes if that is what you want")
		}
		c, err := daemon.Connect()
		if err != nil {
			return err
		}
		fmt.Printf("Reverting %q:\n", id)
		printRevertWarning()
		msg, err := c.RevertFolder(id)
		if err != nil {
			return err
		}
		if msg == "" {
			msg = "restoring this folder from the machine that sent it"
		}
		fmt.Println("\n" + msg)
		fmt.Println("Watch it settle with: backup-maker receive status")
		return nil
	},
}

func printRevertWarning() {
	fmt.Println("  - undo every edit made to these files on this machine")
	fmt.Println("  - DELETE every file added here that the sending machine does not have")
	fmt.Println("  - bring back every file deleted here")
}

func init() {
	receiveEnableCmd.Flags().String("root", "", "directory where backups from other machines land")
	receiveRevertCmd.Flags().Bool("yes", false, "confirm that files added on this machine may be deleted")
	receiveCmd.AddCommand(receiveEnableCmd, receiveDisableCmd, receiveStatusCmd, receiveRevertCmd)
	rootCmd.AddCommand(receiveCmd)
}
