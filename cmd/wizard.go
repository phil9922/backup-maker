// SPDX-License-Identifier: MIT

package cmd

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/phil9922/backup-maker/internal/config"
)

var wizardCmd = &cobra.Command{
	Use:   "wizard",
	Short: "Guided setup: choose folders, exclude files, schedule full-snapshot archives",
	Long: `Walks through your backup setup interactively:
  - select or deselect folders to protect (and exclude files/subfolders within them)
  - set up scheduled snapshots: password-protected (AES-256) zips of
    chosen folders, written to a drive or network-drive target on a timer —
    alongside the always-on real-time mirror.
The password is required for archives and is stored only in the private
state.json on this machine. Changes are picked up by a running daemon within
seconds.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := config.Load()
		if err != nil {
			if os.IsNotExist(err) {
				return errNotInitialized
			}
			return err
		}
		return runWizard(cfg)
	},
}

func runWizard(cfg *config.Config) error {
	in := bufio.NewReader(os.Stdin)
	readLine := func(prompt string) string {
		fmt.Print(prompt)
		line, _ := in.ReadString('\n')
		return strings.TrimSpace(line)
	}

	fmt.Println("backup-maker setup wizard — press Enter on any menu to keep things as they are.")

	// ----- Step 1: folders (select / deselect / exclude) -----
	for {
		fmt.Println()
		fmt.Println("Folders being backed up:")
		if len(cfg.Folders) == 0 {
			fmt.Println("  (none yet)")
		}
		for i, f := range cfg.Folders {
			ex := ""
			if len(f.ExtraIgnore) > 0 {
				ex = "  (excludes: " + strings.Join(f.ExtraIgnore, ", ") + ")"
			}
			fmt.Printf("  %d) %s  [%s]%s\n", i+1, f.Path, f.Label, ex)
		}
		fmt.Println("  a) add a folder    x) exclude files/subfolders in a folder    r) remove a folder")
		choice := strings.ToLower(readLine("folders> "))
		switch {
		case choice == "":
			goto archives
		case choice == "a":
			p := readLine("Folder path to back up: ")
			if p == "" {
				continue
			}
			abs, err := filepath.Abs(expandHome(p))
			if err != nil {
				fmt.Println("  bad path:", err)
				continue
			}
			if fi, err := os.Stat(abs); err != nil || !fi.IsDir() {
				fmt.Println("  not a directory:", abs)
				continue
			}
			dup := false
			for _, f := range cfg.Folders {
				if f.Path == abs {
					fmt.Println("  already backed up")
					dup = true
				}
			}
			if dup {
				continue
			}
			label := readLine("Label (Enter for \"" + filepath.Base(abs) + "\"): ")
			if label == "" {
				label = filepath.Base(abs)
			}
			cfg.Folders = append(cfg.Folders, config.Folder{
				ID: config.NewFolderID(), Path: abs, Label: label,
			})
			fmt.Println("  added:", abs)
		case choice == "x":
			idx := pickIndex(readLine("Which folder number? "), len(cfg.Folders))
			if idx < 0 {
				continue
			}
			fmt.Println("  Current excludes:", strings.Join(cfg.Folders[idx].ExtraIgnore, ", "))
			fmt.Println("  Enter patterns separated by commas — a name matches anywhere (\"scratch\",")
			fmt.Println("  \"*.iso\"), a path matches that subpath (\"assets/raw\"). Empty clears them.")
			raw := readLine("  excludes> ")
			var pats []string
			for _, p := range strings.Split(raw, ",") {
				if p = strings.TrimSpace(p); p != "" {
					pats = append(pats, p)
				}
			}
			cfg.Folders[idx].ExtraIgnore = pats
			fmt.Println("  excludes updated")
		case choice == "r":
			idx := pickIndex(readLine("Which folder number to remove? "), len(cfg.Folders))
			if idx < 0 {
				continue
			}
			removed := cfg.Folders[idx]
			cfg.Folders = append(cfg.Folders[:idx], cfg.Folders[idx+1:]...)
			// Drop dangling references.
			for ti := range cfg.Targets {
				cfg.Targets[ti].Folders = removeString(cfg.Targets[ti].Folders, removed.ID)
			}
			for ai := range cfg.Archives {
				cfg.Archives[ai].Folders = removeString(cfg.Archives[ai].Folders, removed.ID)
			}
			fmt.Println("  removed:", removed.Path, "(existing backups on targets stay put)")
		default:
			fmt.Println("  a, x, r, or Enter to continue")
		}
	}

archives:
	// ----- Step 2: scheduled full-snapshot archives -----
	state, err := config.LoadState()
	if err != nil {
		return err
	}
	for {
		fmt.Println()
		fmt.Println("Scheduled snapshots (password-protected zips, frozen at one moment):")
		if len(cfg.Archives) == 0 {
			fmt.Println("  (none yet — the real-time mirror still protects your folders continuously)")
		}
		for i, a := range cfg.Archives {
			scope := "all folders"
			if len(a.Folders) > 0 {
				scope = fmt.Sprintf("%d folder(s)", len(a.Folders))
			}
			keep := a.Keep
			if keep <= 0 {
				keep = config.DefaultArchiveKeep
			}
			fmt.Printf("  %d) %s: %s, every %s -> target %q, keep %d\n", i+1, a.Name, scope, a.Every, a.Target, keep)
		}
		fmt.Println("  a) add a scheduled backup    r) remove one")
		choice := strings.ToLower(readLine("archives> "))
		switch {
		case choice == "":
			goto save
		case choice == "a":
			if err := wizardAddArchive(cfg, state, in, readLine); err != nil {
				fmt.Println(" ", err)
			}
		case choice == "r":
			idx := pickIndex(readLine("Which number to remove? "), len(cfg.Archives))
			if idx < 0 {
				continue
			}
			name := cfg.Archives[idx].Name
			cfg.Archives = append(cfg.Archives[:idx], cfg.Archives[idx+1:]...)
			delete(state.ArchivePasswords, name)
			fmt.Println("  removed", name, "(existing zip files on the target stay put)")
		default:
			fmt.Println("  a, r, or Enter to finish")
		}
	}

save:
	if err := cfg.Save(); err != nil {
		return err
	}
	if err := state.Save(); err != nil {
		return err
	}
	fmt.Println()
	fmt.Printf("Saved: %d folder(s), %d target(s), %d scheduled backup(s).\n",
		len(cfg.Folders), len(cfg.Targets), len(cfg.Archives))
	fmt.Println("A running daemon applies this within seconds; check with: backup-maker status")
	return nil
}

func wizardAddArchive(cfg *config.Config, state *config.State, in *bufio.Reader, readLine func(string) string) error {
	// Eligible destinations: drive and share targets.
	var eligible []config.Target
	for _, t := range cfg.Targets {
		if t.Type == "drive" || t.Type == "share" {
			eligible = append(eligible, t)
		}
	}
	if len(eligible) == 0 {
		return fmt.Errorf("no drive or network-drive target to write archives to — add one first (backup-maker add-target)")
	}
	if len(cfg.Folders) == 0 {
		return fmt.Errorf("no folders configured yet — add folders in step 1 first")
	}

	name := readLine("Name for this scheduled backup (e.g. \"weekly-code\"): ")
	if name == "" {
		return fmt.Errorf("cancelled")
	}
	for _, a := range cfg.Archives {
		if a.Name == name {
			return fmt.Errorf("an archive named %q already exists", name)
		}
	}

	fmt.Println("Include which folders?")
	fmt.Println("  0) all folders")
	for i, f := range cfg.Folders {
		fmt.Printf("  %d) %s [%s]\n", i+1, f.Path, f.Label)
	}
	var folderIDs []string
	sel := readLine("Numbers separated by commas (Enter = all): ")
	if sel != "" && sel != "0" {
		for _, part := range strings.Split(sel, ",") {
			n, err := strconv.Atoi(strings.TrimSpace(part))
			if err != nil || n < 1 || n > len(cfg.Folders) {
				return fmt.Errorf("bad folder number %q", part)
			}
			folderIDs = append(folderIDs, cfg.Folders[n-1].ID)
		}
	}

	every := readLine("How often? (\"hourly\", \"daily\", \"weekly\", or e.g. \"12h\"): ")
	if _, err := config.ParseEvery(every); err != nil {
		return err
	}

	fmt.Println("Write archives to which target?")
	for i, t := range eligible {
		loc := t.Path
		if t.Type == "share" {
			loc = t.URL
		}
		fmt.Printf("  %d) %s (%s, %s)\n", i+1, t.Name, t.Type, loc)
	}
	idx := pickIndex(readLine("> "), len(eligible))
	if idx < 0 {
		return fmt.Errorf("cancelled")
	}

	keep := config.DefaultArchiveKeep
	if k := readLine(fmt.Sprintf("How many archives to keep? (Enter = %d): ", keep)); k != "" {
		n, err := strconv.Atoi(k)
		if err != nil || n < 1 {
			return fmt.Errorf("keep must be a number >= 1")
		}
		keep = n
	}

	fmt.Println("A password is REQUIRED — archives are AES-256 encrypted zips.")
	fmt.Println("If you lose this password, the archives cannot be opened. Store it somewhere safe.")
	pass, err := promptPasswordVia(in, "Archive password: ")
	if err != nil {
		return err
	}
	if pass == "" {
		return fmt.Errorf("a password is required for archives")
	}
	confirm, err := promptPasswordVia(in, "Repeat password: ")
	if err != nil {
		return err
	}
	if pass != confirm {
		return fmt.Errorf("passwords do not match")
	}

	cfg.Archives = append(cfg.Archives, config.Archive{
		Name: name, Folders: folderIDs, Every: every, Target: eligible[idx].Name, Keep: keep,
	})
	if state.ArchivePasswords == nil {
		state.ArchivePasswords = map[string]string{}
	}
	state.ArchivePasswords[name] = pass
	fmt.Printf("  added %q — first run happens within a minute of the daemon seeing it.\n", name)
	fmt.Println("  Archives can be opened with 7-Zip, WinRAR, Keka, etc. (not Windows Explorer's")
	fmt.Println("  built-in zip viewer, which can't read AES encryption).")
	return nil
}

func pickIndex(s string, n int) int {
	i, err := strconv.Atoi(strings.TrimSpace(s))
	if err != nil || i < 1 || i > n {
		if strings.TrimSpace(s) != "" {
			fmt.Println("  no such number")
		}
		return -1
	}
	return i - 1
}

func removeString(list []string, s string) []string {
	out := list[:0]
	for _, v := range list {
		if v != s {
			out = append(out, v)
		}
	}
	return out
}

func expandHome(p string) string {
	if p == "~" || strings.HasPrefix(p, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, strings.TrimPrefix(p, "~"))
		}
	}
	return p
}

func init() {
	rootCmd.AddCommand(wizardCmd)
}
