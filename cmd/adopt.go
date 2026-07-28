// SPDX-License-Identifier: MIT

package cmd

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/phil9922/backup-maker/internal/setup"
)

var adoptCmd = &cobra.Command{
	Use:   "adopt <destination>",
	Short: "Set up this machine from an existing backup destination",
	Long: `Point a fresh install at a destination that already holds backups — a
drive, SD card, USB stick, or a network share (//host/share) — and rebuild this
machine's configuration from the manifest stored there: folders, excludes,
every destination, and scheduled snapshots.

Secrets are never written to a destination, so adoption asks for the share and
archive passwords you already hold. You can continue as the original machine, so
backups keep building in the same place, or start fresh as a new one.`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return runAdopt(args[0])
	},
}

func runAdopt(dest string) error {
	if err := setup.AdoptAllowed(); err != nil {
		return err
	}

	in := bufio.NewReader(os.Stdin)
	readLine := func(prompt string) string {
		fmt.Print(prompt)
		line, _ := in.ReadString('\n')
		return strings.TrimSpace(line)
	}

	// Locate the source. A network share needs credentials up front just to
	// read its manifest; a local path opens directly.
	var src setup.AdoptSource
	if strings.HasPrefix(dest, "//") {
		fmt.Println("That destination is a network share. Enter its credentials:")
		user := readLine("  Username (Enter for guest): ")
		var pass string
		if user != "" {
			p, err := promptPasswordVia(in, "  Password: ")
			if err != nil {
				return err
			}
			pass = p
		}
		src = setup.AdoptSource{URL: dest, Username: user, Password: pass}
	} else {
		src = setup.AdoptSource{Path: dest}
	}

	insp, err := setup.InspectSource(src)
	if err != nil {
		return err
	}

	fmt.Println()
	fmt.Printf("This destination holds backups from machine %q.\n", insp.MachineName)
	fmt.Printf("  Folders being backed up: %d\n", len(insp.Folders))
	fmt.Printf("  Destinations configured: %d\n", len(insp.Targets))
	fmt.Printf("  Scheduled snapshots:     %d\n", len(insp.Archives))
	fmt.Println()

	dec := setup.AdoptDecisions{
		PathRemap:        map[string]string{},
		SharePasswords:   map[string]string{},
		ArchivePasswords: map[string]string{},
	}

	// ---- machine identity ----
	ans := strings.ToLower(readLine(fmt.Sprintf(
		"Continue as %q so backups keep building in the same place? [Y/n]: ", insp.MachineName)))
	if ans == "" || ans == "y" || ans == "yes" {
		dec.ContinueAsMachine = true
	} else {
		host, _ := os.Hostname()
		dec.NewMachineName = readLine(fmt.Sprintf("New machine name (Enter for %q): ", host))
		fmt.Println("  Starting fresh: history will build in a separate area on each destination.")
	}

	// ---- folder source paths (remap for a new user or OS) ----
	if len(insp.Folders) > 0 {
		fmt.Println()
		fmt.Println("Source folders on THIS machine:")
		for _, f := range insp.Folders {
			note := ""
			if !f.Exists {
				note = "  (not found here — set the new location)"
			}
			fmt.Printf("  %s -> %s%s\n", f.Label, f.Path, note)
			np := readLine("    new path (Enter to keep): ")
			if np == "" {
				continue
			}
			abs, err := filepath.Abs(expandHome(np))
			if err != nil {
				fmt.Println("    bad path, keeping the original:", err)
				continue
			}
			dec.PathRemap[f.ID] = abs
		}
	}

	// ---- passwords (never stored at a destination) ----
	for _, t := range insp.Targets {
		if t.Type != "share" || t.PointedAt {
			continue // the pointed-at share reuses the credentials just entered
		}
		fmt.Println()
		fmt.Printf("Network share %q (%s) needs its password", t.Name, t.Location)
		if t.Username != "" {
			fmt.Printf(" for user %q", t.Username)
		}
		fmt.Println(":")
		p, err := promptPasswordVia(in, "  Password (Enter to skip for now): ")
		if err != nil {
			return err
		}
		if p != "" {
			dec.SharePasswords[t.Name] = p
		}
	}
	for _, a := range insp.Archives {
		fmt.Println()
		fmt.Printf("Scheduled snapshot %q is an encrypted zip — enter its password:\n", a.Name)
		p, err := promptPasswordVia(in, "  Password (Enter to skip for now): ")
		if err != nil {
			return err
		}
		if p != "" {
			dec.ArchivePasswords[a.Name] = p
		}
	}

	res, err := setup.AdoptFromSource(src, dec)
	if err != nil {
		return err
	}

	fmt.Println()
	fmt.Printf("Adopted as machine %q: %d folder(s), %d destination(s), %d scheduled snapshot(s).\n",
		res.MachineName, res.Folders, res.Targets, res.Archives)
	if len(res.MissingUUIDs) > 0 {
		fmt.Println("  Had no recorded id, must be re-added:", strings.Join(res.MissingUUIDs, ", "))
	}
	if len(res.SharesNeedingPassword) > 0 {
		fmt.Println("  No password entered for:", strings.Join(res.SharesNeedingPassword, ", "))
		fmt.Println("  They stay idle until: backup-maker set-password <name>")
	}
	fmt.Println("A running daemon applies this within seconds; check with: backup-maker status")
	return nil
}

func init() {
	rootCmd.AddCommand(adoptCmd)
}
