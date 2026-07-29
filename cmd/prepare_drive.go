// SPDX-License-Identifier: MIT

package cmd

import (
	"encoding/json"
	"fmt"
	"io"
	"os"

	"github.com/phil9922/backup-maker/internal/drivesetup"

	"github.com/spf13/cobra"
)

// prepareOpts is one request to turn a blank disk into a backup destination.
type prepareOpts struct {
	device       string
	mount        string
	label        string
	confirm      string
	dryRun       bool
	force        bool
	installRules bool
}

var prepareDriveCmd = &cobra.Command{
	Use:   "prepare-drive",
	Short: "Format a blank drive and mount it, so it can hold backups",
	Long: `Turns a drive with nothing on it into a mounted backup destination:
one partition, an ext4 filesystem, an /etc/fstab entry by UUID with "nofail"
so the machine still boots if the drive is missing, and the mount itself.

THIS ERASES THE DRIVE, so it refuses to run on a drive with anything on it at
all. It also refuses a drive that has anything mounted on it, a drive holding a
folder this computer backs up, and any device that is not a whole disk. You
must pass --confirm with the phrase the dashboard shows for that drive.

Partitioning needs root, so this is normally reached one of two ways:

  sudo backup-maker prepare-drive --device /dev/sda --mount /mnt/backups \
      --label BACKUPS --confirm "sda 465.8GB"

or from the dashboard, which runs exactly that for you once you have allowed
it with:

  sudo backup-maker prepare-drive --install-sudoers

Run it with --dry-run first: that prints every command it would run and
changes nothing.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		var o prepareOpts
		o.device, _ = cmd.Flags().GetString("device")
		o.mount, _ = cmd.Flags().GetString("mount")
		o.label, _ = cmd.Flags().GetString("label")
		o.confirm, _ = cmd.Flags().GetString("confirm")
		o.dryRun, _ = cmd.Flags().GetBool("dry-run")
		o.force, _ = cmd.Flags().GetBool("force")
		o.installRules, _ = cmd.Flags().GetBool("install-sudoers")

		// THE ONLY FORM THE DASHBOARD IS EVER ALLOWED TO RUN AS ROOT, and it
		// takes no arguments, so the sudoers rule that permits it needs no
		// wildcard. Everything about the drive arrives on stdin, where it
		// cannot widen what sudo agreed to.
		//
		// Anything set from a flag is therefore discarded here — --force above
		// all, which skips the "there is already something on it" refusal. It
		// has no field in the request and no way in through this door; it
		// remains available to somebody typing their own password at a
		// terminal, which is a person who has already proved who they are.
		if fromStdin, _ := cmd.Flags().GetBool("from-stdin"); fromStdin {
			var r drivesetup.Request
			if err := json.NewDecoder(io.LimitReader(os.Stdin, 64<<10)).Decode(&r); err != nil {
				return fmt.Errorf("could not read the request: %w", err)
			}
			// A do-nothing probe, used by the daemon to find out whether sudo
			// will run this without asking for a password. There is no way to
			// ask sudo that directly: "sudo -l <cmd>" answers whether the
			// command is PERMITTED, which is true for any member of the sudo
			// group and says nothing about whether a password is needed.
			// Invoking it is the only honest test — and the probe rides this
			// same command so that asking costs exactly what doing costs, and
			// the rule stays one exact line.
			if os.Geteuid() != 0 {
				return fmt.Errorf("not running as root")
			}
			if r.Probe {
				return nil
			}
			return prepareDrive(prepareOpts{
				device: r.Device, mount: r.Mount, label: r.Label, confirm: r.Confirm,
			}, os.Stdout)
		}
		if o.installRules {
			return installSudoers(os.Stdout)
		}
		if o.device == "" || o.mount == "" {
			return fmt.Errorf("--device and --mount are both required (try --help)")
		}
		return prepareDrive(o, os.Stdout)
	},
}

func init() {
	prepareDriveCmd.Flags().String("device", "", "the whole disk to prepare, e.g. /dev/sda")
	prepareDriveCmd.Flags().String("mount", "", "where to mount it, e.g. /mnt/backups")
	prepareDriveCmd.Flags().String("label", "BACKUPS", "filesystem label")
	prepareDriveCmd.Flags().String("confirm", "", "the phrase shown for this drive, typed back")
	prepareDriveCmd.Flags().Bool("dry-run", false, "print what would be done and change nothing")
	prepareDriveCmd.Flags().Bool("force", false, "allow a drive that already has something on it (NOT reachable from the dashboard)")
	prepareDriveCmd.Flags().Bool("install-sudoers", false, "allow the dashboard to run this command, after showing you the rule")
	prepareDriveCmd.Flags().Bool("from-stdin", false, "read the request as JSON on stdin; the only form the dashboard runs as root")
	_ = prepareDriveCmd.Flags().MarkHidden("from-stdin")
	rootCmd.AddCommand(prepareDriveCmd)
}
