// SPDX-License-Identifier: MIT

package cmd

import (
	"bufio"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/phil9922/backup-maker/internal/config"
	"github.com/phil9922/backup-maker/internal/setup"
	"github.com/phil9922/backup-maker/internal/smbfs"
	"github.com/phil9922/backup-maker/internal/wol"
)

var addTargetCmd = &cobra.Command{
	Use:   "add-target",
	Short: "Add a backup destination",
}

func checkNameFree(cfg *config.Config, name string) error {
	for _, t := range cfg.Targets {
		if t.Name == name {
			return fmt.Errorf("a target named %q already exists", name)
		}
	}
	return nil
}

var addTargetDriveCmd = &cobra.Command{
	Use:   "drive <mount-path>",
	Short: "Back up onto a drive attached to this computer (SD card, USB stick, external disk)",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if !config.Exists() {
			return errNotInitialized
		}
		name, _ := cmd.Flags().GetString("name")
		t, err := setup.AddDriveTarget(args[0], name)
		if err != nil {
			if os.IsNotExist(err) {
				return errNotInitialized
			}
			return err
		}
		fmt.Printf("Added drive target %q at %s — it now mirrors every backed-up folder.\n", t.Name, t.Path)
		return nil
	},
}

var addTargetShareCmd = &cobra.Command{
	Use:   "share <//host/share[/subfolder]>",
	Short: "Back up onto a network drive (NAS, router USB drive, or a folder shared by another computer)",
	Long: `Back up onto any SMB network drive on your local network — a NAS, a USB
drive plugged into your router, or a folder shared from a Windows/Mac/Linux
machine. Find addresses with: backup-maker scan

Every file written to a network drive is read back and checksum-verified by
default (disable with --no-verify).`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := config.Load()
		if err != nil {
			if os.IsNotExist(err) {
				return errNotInitialized
			}
			return err
		}
		url := args[0]
		if _, _, _, _, err := smbfs.Parse(url); err != nil {
			return err
		}
		name, _ := cmd.Flags().GetString("name")
		user, _ := cmd.Flags().GetString("user")
		pass := ""
		if user != "" {
			pass, err = promptPassword(fmt.Sprintf("Password for %s on %s: ", user, url))
			if err != nil {
				return err
			}
		}
		_ = cfg

		fmt.Println("Testing connection…")
		noVerify, _ := cmd.Flags().GetBool("no-verify")
		if err := setup.AddShareTarget(url, user, pass, name, !noVerify); err != nil {
			if user == "" {
				return fmt.Errorf("%w\n(if the share is locked, retry with --user <name>)", err)
			}
			return err
		}
		fmt.Printf("Added network drive %s — it now mirrors every backed-up folder.\n", url)
		if !noVerify {
			fmt.Println("Every written file will be read back and checksum-verified.")
		}
		// setup.AddShareTarget owns creating the target (it is shared with the
		// dashboard), so Wake-on-LAN is attached afterwards by name.
		if raw, _ := cmd.Flags().GetString("mac"); raw != "" {
			mac, err := wol.ParseMAC(raw)
			if err != nil {
				return err
			}
			fresh, err := config.Load()
			if err != nil {
				return err
			}
			applied := false
			for i := range fresh.Targets {
				if fresh.Targets[i].Type == "share" && fresh.Targets[i].URL == url {
					fresh.Targets[i].MAC = mac.String()
					applied = true
				}
			}
			if !applied {
				return fmt.Errorf("target added, but could not attach the MAC; set it with: backup-maker set-mac <name> %s", raw)
			}
			if err := fresh.Save(); err != nil {
				return err
			}
			fmt.Printf("Wake-on-LAN enabled (%s) — see the README for the setup it needs on that machine.\n", mac)
		}
		return nil
	},
}

func promptPassword(prompt string) (string, error) {
	return promptPasswordVia(nil, prompt)
}

// promptPasswordVia reads a password without echo on a terminal. In
// non-interactive use (tests, pipes) it reads a line — from r when the caller
// already owns a buffered reader on stdin (mixing bufio and direct stdin
// reads loses buffered input), else from stdin directly.
func promptPasswordVia(r *bufio.Reader, prompt string) (string, error) {
	fmt.Print(prompt)
	if term.IsTerminal(int(os.Stdin.Fd())) {
		data, err := term.ReadPassword(int(os.Stdin.Fd()))
		fmt.Println()
		return string(data), err
	}
	if r == nil {
		r = bufio.NewReader(os.Stdin)
	}
	line, err := r.ReadString('\n')
	line = strings.TrimRight(line, "\r\n")
	if err != nil && line == "" {
		return "", err
	}
	return line, nil
}

var addTargetDeviceCmd = &cobra.Command{
	Use:   "device <DEVICE-ID>",
	Short: "Back up to another computer on your network running backup-maker",
	Long: `On the other computer, run "backup-maker receive enable --root <path>" and
"backup-maker pair" to see its device ID. Then pass that ID here. The other
machine must approve this machine once (backup-maker pair accept).`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := config.Load()
		if err != nil {
			if os.IsNotExist(err) {
				return errNotInitialized
			}
			return err
		}
		id := strings.ToUpper(strings.TrimSpace(args[0]))
		if len(id) < 50 || strings.Count(id, "-") < 7 {
			return fmt.Errorf("that doesn't look like a device ID (expected XXXXXXX-XXXXXXX-... from \"backup-maker pair\" on the other machine)")
		}
		name, _ := cmd.Flags().GetString("name")
		if name == "" {
			name = "machine-" + strings.SplitN(id, "-", 2)[0]
		}
		for _, t := range cfg.Targets {
			if t.Type == "device" && t.DeviceID == id {
				return fmt.Errorf("that machine is already a target (%q)", t.Name)
			}
		}
		if err := checkNameFree(cfg, name); err != nil {
			return err
		}
		target := config.Target{
			Type: "device", Name: name, DeviceID: id, Folders: []string{},
		}
		if raw, _ := cmd.Flags().GetString("mac"); raw != "" {
			mac, err := wol.ParseMAC(raw)
			if err != nil {
				return err
			}
			target.MAC = mac.String()
		}
		cfg.Targets = append(cfg.Targets, target)
		if err := cfg.Save(); err != nil {
			return err
		}
		fmt.Printf("Added machine target %q.\n", name)
		if target.MAC != "" {
			fmt.Printf("Wake-on-LAN enabled (%s) — see the README for the setup it needs on that machine.\n", target.MAC)
		}
		fmt.Println("Finish pairing on the other machine with: backup-maker pair accept " + shortDeviceID(id))
		return nil
	},
}

func shortDeviceID(id string) string {
	if i := strings.IndexByte(id, '-'); i > 0 {
		return id[:i] + "-…"
	}
	return id
}

func init() {
	addTargetDriveCmd.Flags().String("name", "", "target name (default: mount point basename)")
	addTargetShareCmd.Flags().String("name", "", "target name (default: share name)")
	addTargetShareCmd.Flags().String("user", "", "username for the share (omit to try guest access)")
	addTargetShareCmd.Flags().Bool("no-verify", false, "skip read-back checksum verification of written files")
	addTargetShareCmd.Flags().String("mac", "", "wired MAC address of the host, to wake it when asleep (Wake-on-LAN)")
	addTargetDeviceCmd.Flags().String("name", "", "target name (default: machine-<id prefix>)")
	addTargetDeviceCmd.Flags().String("mac", "", "wired MAC address of that machine, to wake it when asleep (Wake-on-LAN)")
	addTargetCmd.AddCommand(addTargetDriveCmd, addTargetShareCmd, addTargetDeviceCmd)
	rootCmd.AddCommand(addTargetCmd)
}
