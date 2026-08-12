// SPDX-License-Identifier: MIT

package cmd

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/phil9922/backup-maker/internal/daemon"
	"github.com/phil9922/backup-maker/internal/status"
)

// backup-now runs a backup that is already configured, right now.
//
// THROUGH THE DAEMON, like every other command that makes something happen. The
// daemon owns the mirror engines and the snapshot runner; a CLI that started its
// own engine would be a second writer on the same destination, reconciling the
// same tree against the same source at the same time.
var backupNowCmd = &cobra.Command{
	Use:   "backup-now [folder] [destination]",
	Short: "Run a backup that is already set up, now instead of at its next slot",
	Long: `Run one of the backups you have already configured, immediately.

It does whatever that backup is set up to do — it changes nothing about it.

  backup-maker backup-now photos            # copy "photos" to its destination now
  backup-maker backup-now photos laptopcard # ...to that one, if it has several
  backup-maker backup-now --snapshot nightly

The first form forces a continuous mirror's full check now rather than waiting
for the hourly one. The second writes one timed snapshot now rather than waiting
for its slot; that counts as the schedule's latest run, and only the newest few
snapshots are kept, so it may delete the oldest.

Both return as soon as the work has STARTED. A mirror pass over a network share
takes minutes and a snapshot of a large folder takes tens of them — watch either
with: backup-maker status`,
	Args: cobra.MaximumNArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		snapshot, _ := cmd.Flags().GetString("snapshot")
		c, err := daemon.Connect()
		if err != nil {
			return err
		}

		if snapshot != "" {
			if len(args) > 0 {
				return fmt.Errorf("--snapshot names the schedule on its own; drop %q", args[0])
			}
			msg, err := c.BackUpArchiveNow(snapshot)
			if err != nil {
				return err
			}
			return started(msg)
		}
		if len(args) == 0 {
			return fmt.Errorf("say which folder to back up now, or use --snapshot <name> for a timed snapshot (see: backup-maker status)")
		}

		var m status.Model
		if err := c.Status(&m); err != nil {
			return err
		}
		dest := ""
		if len(args) > 1 {
			dest = args[1]
		}
		// Paired machines are excluded: syncthing keeps those up to date and
		// this button does not drive it, so there is no pass to force.
		row, err := oneMirror(&m, args[0], dest, false)
		if err != nil {
			return err
		}
		msg, err := c.BackUpFolderNow(row.FolderID, row.TargetName)
		if err != nil {
			return err
		}
		return started(msg)
	},
}

func started(msg string) error {
	if msg == "" {
		msg = "started"
	}
	fmt.Println(msg)
	fmt.Println("Watch it with: backup-maker status")
	return nil
}

// oneMirror picks the single folder→destination pair the arguments name.
//
// It REFUSES rather than guessing when more than one matches: a folder with
// three destinations has three separate mirrors, and acting on the wrong one
// leaves somebody believing a drive is up to date when it is not.
//
// devices says whether a paired computer counts as a destination here. It does
// for pausing — that pair is a mirror like any other, kept up to date by the
// sync engine — and it does not for "back up now", which drives the local mirror
// engines and has nothing to force over there.
func oneMirror(m *status.Model, folder, dest string, devices bool) (status.Row, error) {
	var matched []status.Row
	for _, r := range m.Rows {
		if r.TargetName == "" || (!devices && r.TargetType == "device") {
			continue
		}
		if r.FolderID != folder && !strings.EqualFold(r.FolderLabel, folder) {
			continue
		}
		if dest != "" && !strings.EqualFold(r.TargetName, dest) {
			continue
		}
		matched = append(matched, r)
	}
	switch len(matched) {
	case 1:
		return matched[0], nil
	case 0:
		if dest != "" {
			return status.Row{}, fmt.Errorf("nothing copies %q to %q continuously (see: backup-maker status)", folder, dest)
		}
		return status.Row{}, fmt.Errorf("no folder called %q is being copied continuously (see: backup-maker status)", folder)
	}
	var names []string
	for _, r := range matched {
		names = append(names, r.TargetName)
	}
	return status.Row{}, fmt.Errorf("%q is copied to %s; say which one:  backup-maker backup-now %s %s",
		folder, strings.Join(names, ", "), folder, names[0])
}

func init() {
	backupNowCmd.Flags().String("snapshot", "",
		"write this timed snapshot schedule's zip now, instead of a continuous mirror pass")
	rootCmd.AddCommand(backupNowCmd)
}
