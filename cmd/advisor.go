// SPDX-License-Identifier: MIT

package cmd

import (
	"bufio"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/phil9922/backup-maker/internal/config"
)

// The setup advisor is a fully local, vendor-neutral quiz: it recommends
// hardware CATEGORIES and architecture improvements, never products or
// sellers. It sends nothing anywhere and stores only a "seen it" flag so it
// doesn't re-prompt.

var advisorCmd = &cobra.Command{
	Use:   "advisor",
	Short: "Answer 5 quick questions, get tailored backup-setup advice",
	Long: `A 60-second quiz that looks at how your backups are arranged and points out
the single biggest gap. Runs entirely on this machine and sends nothing
anywhere. Vendor-neutral: it suggests kinds of hardware, never brands.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		runAdvisorQuiz()
		return nil
	},
}

// maybeOfferAdvisor runs after a successful init: interactive terminals only,
// once ever (unless re-run explicitly via `backup-maker advisor`).
func maybeOfferAdvisor() {
	if !term.IsTerminal(int(os.Stdin.Fd())) {
		return
	}
	state, err := config.LoadState()
	if err != nil || state.AdvisorSeen {
		return
	}
	state.AdvisorSeen = true
	_ = state.Save()

	fmt.Println()
	fmt.Print("Optional: a 60-second quiz can point out the biggest gap in your backup plan.\n" +
		"It runs entirely on this machine and recommends kinds of hardware, never brands.\n" +
		"Press Enter to skip, or type y to take it: ")
	r := bufio.NewReader(os.Stdin)
	line, _ := r.ReadString('\n')
	if strings.TrimSpace(strings.ToLower(line)) != "y" {
		fmt.Println("Skipped. Run it anytime with: backup-maker advisor")
		return
	}
	runAdvisorQuiz()
}

type advisorAnswers struct {
	devices     int // 0 just this computer, 1 one drive, 2 second computer/NAS, 3 several
	offsite     bool
	alwaysOn    int // 0 no, 1 NAS/server, 2 another computer usually on
	dataSize    int // 0 <100GB, 1 100GB-1TB, 2 more
	sensitivity int // 0 personal, 1 important, 2 highly sensitive
}

func runAdvisorQuiz() {
	r := bufio.NewReader(os.Stdin)
	ask := func(question string, options []string) (int, bool) {
		fmt.Println()
		fmt.Println(question)
		for i, o := range options {
			fmt.Printf("  %d) %s\n", i+1, o)
		}
		for {
			fmt.Print("> ")
			line, err := r.ReadString('\n')
			if err != nil {
				return 0, false
			}
			line = strings.TrimSpace(line)
			if line == "" {
				fmt.Println("(quiz cancelled — run it again anytime with: backup-maker advisor)")
				return 0, false
			}
			n, err := strconv.Atoi(line)
			if err == nil && n >= 1 && n <= len(options) {
				return n - 1, true
			}
			fmt.Printf("Please enter a number from 1 to %d (or press Enter to cancel).\n", len(options))
		}
	}

	var a advisorAnswers
	var ok bool
	if a.devices, ok = ask("How many separate devices can hold backups?", []string{
		"Just this computer",
		"One other drive I can plug in",
		"A second computer or NAS",
		"Several",
	}); !ok {
		return
	}
	var off int
	if off, ok = ask("Is any potential backup location in a different building?", []string{
		"No", "Yes",
	}); !ok {
		return
	}
	a.offsite = off == 1
	if a.alwaysOn, ok = ask("Do you have an always-on device besides your main computer?", []string{
		"No",
		"Yes, a NAS or server",
		"Yes, another computer that's usually on",
	}); !ok {
		return
	}
	if a.dataSize, ok = ask("Roughly how much data are you protecting?", []string{
		"Under 100GB", "100GB–1TB", "More",
	}); !ok {
		return
	}
	if a.sensitivity, ok = ask("How sensitive is this data?", []string{
		"Personal", "Important", "Highly sensitive",
	}); !ok {
		return
	}

	printAdvice(a)
}

func printAdvice(a advisorAnswers) {
	fmt.Println()
	fmt.Println("=== Your backup setup, assessed ===")
	fmt.Println()

	// The single biggest gap, in priority order.
	switch {
	case a.devices == 0:
		fmt.Println("Biggest gap: everything lives on one computer.")
		fmt.Println("A backup target on the same machine is not a real backup — if the disk,")
		fmt.Println("power supply, or the machine itself fails, the \"backup\" goes with it.")
		fmt.Println("The single most important next step: add one separate device, even a")
		fmt.Println(capacitySuggestion(a.dataSize) + " left plugged into this computer.")
	case !a.offsite:
		fmt.Println("Biggest gap: every copy is in one building.")
		fmt.Println("Fire, theft, flood, or a power surge can take out all copies at once —")
		fmt.Println("this is the failure mode people don't see coming. Two supported fixes:")
		fmt.Println("  - a drive you carry to another location, or")
		fmt.Println("  - a drive you rotate: keep one at home, one elsewhere, swap regularly.")
	case a.alwaysOn == 0:
		fmt.Println("Biggest gap: no always-on backup target.")
		fmt.Println("A target that's often powered off only receives backups while it's awake —")
		fmt.Println("your newest work is unprotected in between. Either add an always-on")
		fmt.Println("target (see docs/RECOMMENDED-HARDWARE.md for categories), or make a habit")
		fmt.Println("of powering the target on when you start working.")
	default:
		fmt.Println("Foundation looks solid: multiple devices, an offsite option, and an")
		fmt.Println("always-on target are all available to you.")
		fmt.Println("Highest-value next step: make sure the offsite copy actually stays")
		fmt.Println("current, and check `backup-maker status` for stale (!!) targets weekly.")
	}

	// Capacity guidance (generic categories only).
	fmt.Println()
	fmt.Printf("For your data size, a %s is the right category of hardware.\n", capacitySuggestion(a.dataSize))

	// Sensitivity guidance.
	if a.sensitivity >= 1 {
		fmt.Println()
		fmt.Println("Because this data matters: turn on your operating system's built-in drive")
		fmt.Println("encryption for every backup target (and the source machine).")
		if a.sensitivity == 2 {
			fmt.Println("For highly sensitive data, prefer hardware that stays in your control.")
			fmt.Println("Be cautious with any option that leaves your possession or your network —")
			fmt.Println("an encrypted drive you rotate to a trusted location is the way to do it")
			fmt.Println("sync for this kind of data.")
		}
	}

	fmt.Println()
	fmt.Println("Two things to always remember:")
	fmt.Println("  1. Keep at least one copy offsite — separate building, not just separate device.")
	fmt.Println("  2. Test a restore before you need it. A backup you've never restored from")
	fmt.Println("     is a hope, not a plan.")
	fmt.Println()
	fmt.Println("Hardware categories and buying principles: docs/RECOMMENDED-HARDWARE.md")
}

func capacitySuggestion(size int) string {
	switch size {
	case 0:
		return "high-endurance memory card or USB stick (128GB+)"
	case 1:
		return "external SSD (1–2TB)"
	default:
		return "multi-terabyte external or NAS-class drive"
	}
}

func init() {
	rootCmd.AddCommand(advisorCmd)
}
