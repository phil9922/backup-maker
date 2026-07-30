// SPDX-License-Identifier: MIT

package cmd

import (
	"fmt"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/phil9922/backup-maker/internal/config"
	"github.com/phil9922/backup-maker/internal/webui"
)

var docsExportDir string

var docsCmd = &cobra.Command{
	Use:   "docs",
	Short: "Read the manual, or write it out as a web page",
	Long: `The whole manual is built into this binary.

Normally you read it in the dashboard, under Docs. That needs the daemon
running, which is exactly what you may not have when you most want the
manual — a fresh machine, a rescue boot, or a backup drive in your hand and
nothing else.

  backup-maker docs --export ./manual

writes it out as ordinary web pages: open manual/index.html in any browser,
with no daemon, no web server and no internet. The directory can be copied
anywhere, including onto a backup destination beside the status page.

An existing directory is only written to if it is empty or holds a previous
export. Nothing is ever deleted.`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		if docsExportDir == "" {
			fmt.Println("The manual is built into this binary.")
			fmt.Println()
			fmt.Println("  Read it in the dashboard:  backup-maker web   (then click Docs)")
			fmt.Printf("  Or on this machine:        http://127.0.0.1:%d/docs\n", config.DefaultDashboardPort)
			fmt.Println("  Or write it out to read without the daemon:")
			fmt.Println("                             backup-maker docs --export ./manual")
			return nil
		}
		out, err := webui.ExportDocs(docsExportDir)
		if err != nil {
			return err
		}
		fmt.Printf("Wrote %d pages and %d files to %s\n", out.Pages, out.Assets, out.Dir)
		fmt.Printf("Open it with: %s\n", filepath.Join(out.Dir, "index.html"))
		return nil
	},
}

func init() {
	docsCmd.Flags().StringVar(&docsExportDir, "export", "",
		"write the manual to this directory as web pages")
	rootCmd.AddCommand(docsCmd)
}
