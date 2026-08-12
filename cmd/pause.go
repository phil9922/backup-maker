// SPDX-License-Identifier: MIT

package cmd

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/phil9922/backup-maker/internal/daemon"
	"github.com/phil9922/backup-maker/internal/status"
)

// pause / resume switch one folder's copy to one destination off and on again.
//
// THROUGH THE DAEMON, like every other command that changes what is running. It
// writes config.toml and the daemon's watcher applies it, which is what stops
// the engine for that pair — a CLI that edited the file behind the daemon's back
// would leave the two disagreeing until something else happened to reload.
var pauseCmd = &cobra.Command{
	Use:   "pause <folder> [destination]",
	Short: "Stop backing one folder up to one destination, without losing anything",
	Long: `Stop copying a folder to one of its destinations, leaving the others running.

  backup-maker pause photos             # if "photos" has one destination
  backup-maker pause photos laptopcard  # ...or say which one

NOTHING IS DELETED AND NOTHING IS COPIED. Everything already on that destination
stays exactly where it is; the folder simply stops being copied there until you
resume it. The pair remembers when it was last backed up, so resuming carries on
rather than starting the whole backup again.

Resume with: backup-maker resume photos laptopcard`,
	Args: cobra.RangeArgs(1, 2),
	RunE: func(cmd *cobra.Command, args []string) error { return setPaused(args, true) },
}

var resumeCmd = &cobra.Command{
	Use:   "resume <folder> [destination]",
	Short: "Start backing one folder up to one destination again",
	Long: `Undo a pause: this folder starts copying to that destination again within a few
seconds, carrying on from where it left off.

  backup-maker resume photos laptopcard`,
	Args: cobra.RangeArgs(1, 2),
	RunE: func(cmd *cobra.Command, args []string) error { return setPaused(args, false) },
}

func setPaused(args []string, paused bool) error {
	c, err := daemon.Connect()
	if err != nil {
		return err
	}
	var m status.Model
	if err := c.Status(&m); err != nil {
		return err
	}
	dest := ""
	if len(args) > 1 {
		dest = args[1]
	}
	// Paired computers included: that pair is a mirror too, and pausing it stops
	// the folder being offered to that machine.
	row, err := oneMirror(&m, args[0], dest, true)
	if err != nil {
		return err
	}
	msg, err := c.SetMirrorPaused(row.FolderID, row.TargetName, paused)
	if err != nil {
		return err
	}
	if msg == "" {
		msg = "done"
	}
	fmt.Printf("%s → %s: %s\n", row.FolderLabel, row.TargetName, msg)
	return nil
}

func init() {
	rootCmd.AddCommand(pauseCmd)
	rootCmd.AddCommand(resumeCmd)
}
