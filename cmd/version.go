// SPDX-License-Identifier: MIT

package cmd

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/phil9922/backup-maker/internal/version"
)

var versionCmd = &cobra.Command{
	Use:   "version",
	Short: "Print the version and build details",
	Long: `Print which build of backup-maker this is.

Release binaries report the released version, its commit and its build time.
A binary you built yourself reports "dev" plus whatever the Go toolchain
recorded about your working tree — including whether it had uncommitted
changes. Include this output in bug reports.`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		i := version.Get()

		v := i.Version
		if i.Dirty {
			v += "-dirty"
		}
		fmt.Printf("backup-maker %s\n", v)
		if i.Commit != "" {
			fmt.Printf("  commit:  %s\n", i.Commit)
		}
		if i.Date != "" {
			fmt.Printf("  built:   %s\n", i.Date)
		}
		fmt.Printf("  runtime: %s %s/%s\n", i.Go, i.OS, i.Arch)
		return nil
	},
}

func init() {
	rootCmd.Version = version.Short()
	rootCmd.SetVersionTemplate("backup-maker {{.Version}}\n")
	rootCmd.AddCommand(versionCmd)
}
