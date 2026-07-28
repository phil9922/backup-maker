// SPDX-License-Identifier: MIT

package cmd

import (
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/phil9922/backup-maker/internal/config"
	"github.com/phil9922/backup-maker/internal/daemon"
	"github.com/phil9922/backup-maker/internal/wol"
)

var wakeCmd = &cobra.Command{
	Use:   "wake <target>",
	Short: "Send a Wake-on-LAN packet to a sleeping backup target",
	Long: `Broadcast a Wake-on-LAN "magic packet" to a target that has a MAC address
configured (see: backup-maker set-mac).

Waking is best-effort. The packet is fire-and-forget UDP: there is no reply,
and a successful send does NOT mean the machine woke up. It will not work
unless Wake-on-LAN is enabled in the target's BIOS/UEFI and operating system,
and it very rarely works over wifi. See the Wake-on-LAN section of the README
for per-OS setup.

Once the daemon has a MAC for a target it also wakes it automatically while
that target is offline, so this command is mainly for testing your setup.`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		c, err := daemon.Connect()
		if err != nil {
			return err
		}
		msg, err := c.Wake(args[0])
		if err != nil {
			return err
		}
		if msg == "" {
			msg = "wake packet sent"
		}
		fmt.Printf("%s\n", msg)
		fmt.Println("Check it came back with: backup-maker status")
		return nil
	},
}

var setMACCmd = &cobra.Command{
	Use:   "set-mac <target> <mac|none>",
	Short: "Enable Wake-on-LAN for a target by recording its network adapter address",
	Long: `Record the MAC (hardware) address of the computer or NAS behind a target so
backup-maker can try to wake it when it's asleep. Pass "none" to turn
Wake-on-LAN back off.

Use the address of the adapter that stays powered during sleep — in practice
the WIRED ethernet adapter. Wifi adapters almost never support waking.

Find it on the target machine:
  Linux    ip link                      (look for "link/ether")
  macOS    ifconfig en0 | grep ether
  Windows  getmac /v                    (or: ipconfig /all → "Physical Address")

Setting a MAC does not by itself make waking work; you must also enable
Wake-on-LAN in the target's BIOS/UEFI and OS. See the README.`,
	Args: cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := config.Load()
		if err != nil {
			if os.IsNotExist(err) {
				return errNotInitialized
			}
			return err
		}
		name, raw := args[0], args[1]

		idx := -1
		for i, t := range cfg.Targets {
			if t.Name == name {
				idx = i
				break
			}
		}
		if idx < 0 {
			return fmt.Errorf("no target named %q (see: backup-maker status)", name)
		}
		if cfg.Targets[idx].Type == "drive" {
			return fmt.Errorf("target %q is a drive attached to this computer; there is nothing to wake", name)
		}

		if strings.EqualFold(raw, "none") || raw == "" {
			cfg.Targets[idx].MAC = ""
			cfg.Targets[idx].WakeBroadcast = ""
			if err := cfg.Save(); err != nil {
				return err
			}
			fmt.Printf("Wake-on-LAN disabled for %q.\n", name)
			return nil
		}

		mac, err := wol.ParseMAC(raw)
		if err != nil {
			return err
		}
		cfg.Targets[idx].MAC = mac.String()
		if bc, _ := cmd.Flags().GetString("broadcast"); bc != "" {
			cfg.Targets[idx].WakeBroadcast = bc
		}
		if err := cfg.Save(); err != nil {
			return err
		}

		fmt.Printf("Wake-on-LAN enabled for %q (%s).\n", name, mac)
		fmt.Println("backup-maker will now try to wake it whenever it's offline.")
		fmt.Println()
		fmt.Println("This only works if the target itself is set up for it:")
		fmt.Println("  1. Enable Wake-on-LAN in its BIOS/UEFI (often \"Power On By PCI-E\").")
		fmt.Println("  2. Enable it in its OS — see the README's Wake-on-LAN section.")
		fmt.Println("  3. Use a WIRED connection; wifi almost never wakes.")
		fmt.Printf("Test it: put the machine to sleep, then run: backup-maker wake %s\n", name)
		return nil
	},
}

func init() {
	setMACCmd.Flags().String("broadcast", "",
		"override the broadcast address for this target (e.g. 192.168.1.255); rarely needed")
	rootCmd.AddCommand(wakeCmd)
	rootCmd.AddCommand(setMACCmd)
}
