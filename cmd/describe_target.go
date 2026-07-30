// SPDX-License-Identifier: MIT

package cmd

import (
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/phil9922/backup-maker/internal/config"
	"github.com/phil9922/backup-maker/internal/setup"
)

var describeTargetCmd = &cobra.Command{
	Use:   "describe-target <name> [description]",
	Short: "Record which physical drive a destination is",
	Long: `Say which drive a destination actually is, in your own words.

  backup-maker describe-target laptopcard "Samsung 128GB card, lives in the laptop"

The text is written onto the destination itself, in its marker file, so it
travels with the drive: it is still there after a reinstall, and it is what
answers "which of these two identical cards is this?" when you are holding one
and the computer that wrote it is dead. That also means the destination has to
be reachable for this to work.

It is not read over the network by anything and is deliberately kept off the
read-only network view. Give no description to clear it.`,
	Args: cobra.RangeArgs(1, 2),
	RunE: func(cmd *cobra.Command, args []string) error {
		if _, err := config.Load(); err != nil {
			if os.IsNotExist(err) {
				return errNotInitialized
			}
			return err
		}
		name := args[0]
		text := ""
		if len(args) == 2 {
			text = strings.TrimSpace(args[1])
		}
		if err := setup.DescribeTarget(name, text); err != nil {
			return err
		}
		if text == "" {
			fmt.Printf("Cleared the description on %q.\n", name)
			return nil
		}
		fmt.Printf("Wrote it onto %q: %s\n", name, text)
		return nil
	},
}

func init() {
	rootCmd.AddCommand(describeTargetCmd)
}
