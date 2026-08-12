// SPDX-License-Identifier: MIT

package cmd

import (
	"fmt"
	"runtime"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/phil9922/backup-maker/internal/config"
	"github.com/phil9922/backup-maker/internal/daemon"
)

var keychainCmd = &cobra.Command{
	Use:   "keychain",
	Short: "Optionally keep stored passwords in the OS keyring instead of state.json",
	Long: `Move the passwords backup-maker stores into your operating system's keyring.

Two kinds of password are stored: the login for each network drive, and the
password on each scheduled snapshot's encrypted zip. By default they live in
state.json, which is mode 0600 in your own config directory — readable by your
account and nothing else. This command moves them into GNOME Keyring / KWallet
on Linux, the Keychain on macOS, or the Credential Manager on Windows, leaving
only a placeholder in the file.

It is OPTIONAL, and it is off until you turn it on. The keyring buys encryption
at rest for a file only your account can read; what it costs is availability,
and that is the thing this program cannot trade away. A headless machine — a
Raspberry Pi backup target — has no keyring at all. A daemon started at boot
rather than at login often has no keyring SESSION, so the keyring exists and
still refuses to answer. A locked keyring behaves the same way. In any of those
cases the passwords are unreachable and the destinations that need them stop
working until the keyring can be read, which is why 'keychain enable' tests
your machine for real before it moves anything, and why 'keychain disable' puts
everything back.

Nothing else moves. The IPC token, the sync engine's API key, the webhook
address and the ntfy token stay in state.json: they are needed on every start,
including the starts where no keyring answers.`,
}

var keychainEnableCmd = &cobra.Command{
	Use:   "enable",
	Short: "Move stored passwords into the OS keyring",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := refuseWhileDaemonRuns("enable"); err != nil {
			return err
		}
		// The whole migration runs inside one update, so the flag and the
		// redacted values land together and nothing else can be writing state.json
		// underneath it. Safe to hold for a keyring round trip per password: this
		// command refuses to run while the daemon is up.
		var entries []secretEntry
		if _, err := config.UpdateState(func(state *config.State) error {
			// THE FIRST THING, BEFORE ANYTHING MOVES. This is the headless-Pi path and
			// the boot-started-daemon path, and it is a round trip rather than a
			// capability check because every way this fails is a way that only appears
			// when it is tried. Refusing here costs the user nothing; discovering it
			// after the migration would cost them every password.
			if err := config.KeyringProbe(); err != nil {
				return fmt.Errorf(`this machine has no usable OS keyring, so the passwords stay in state.json (mode 0600, which is what they have always been):
  %w

That is a normal answer on a headless machine, and on a daemon started at boot
rather than at login — there is no keyring session for it to talk to. Nothing
has been changed`, err)
			}

			entries = secretEntries(state)
			if len(entries) == 0 {
				return config.ErrStateUnchanged
			}

			// MIGRATED AND VERIFIED ONE BY ONE, AND THE FLAG SET ONLY AFTER EVERY LAST
			// ONE READ BACK CORRECTLY. A keyring that accepts a write and returns
			// something else on the read — a size limit silently truncating, a provider
			// that stores per-session — would otherwise be discovered by the mirror
			// engine, hours later, as a wrong password.
			for _, e := range entries {
				if err := config.KeyringPut(e.account, e.secret); err != nil {
					return fmt.Errorf("the keyring would not accept the password for %s %q, so nothing has been changed: %w", e.kind, e.name, err)
				}
				back, err := config.KeyringFetch(e.account)
				if err != nil {
					return fmt.Errorf("the keyring took the password for %s %q but would not give it back, so nothing has been changed: %w", e.kind, e.name, err)
				}
				if back != e.secret {
					return fmt.Errorf("the keyring returned a different password for %s %q than it was given, so nothing has been changed", e.kind, e.name)
				}
			}

			// This is the flag whose save replaces the values in the file with
			// placeholders: Save is where the redaction lives, so there is one
			// place that does it. A failure leaves copies in the keyring and the
			// real passwords still in the file — untidy, and nothing lost.
			state.SecretsInKeyring = true
			return nil
		}); err != nil {
			return err
		}
		if len(entries) == 0 {
			fmt.Println("There are no stored passwords to move.")
			fmt.Println("Run this again after adding a network drive or a scheduled snapshot.")
			return nil
		}

		// Worded as a statement of where things are rather than of what moved,
		// because this command is safe to run twice: on a machine where it is
		// already on, it re-checks every entry and repairs any that a failed
		// keyring write had left in the file.
		fmt.Printf("%d password(s) are in the OS keyring, under the service name %q:\n", len(entries), config.KeyringService)
		for _, e := range entries {
			fmt.Printf("  %-8s %-24s → %s\n", e.kind, e.name, e.account)
		}
		fmt.Println()
		fmt.Println("state.json now holds a placeholder for each one. The names stay in the file:")
		fmt.Println("it is the only record of which passwords exist, because a keyring cannot be")
		fmt.Println("asked to list them.")
		fmt.Println()
		fmt.Println("If the keyring is ever locked or unavailable, the destinations and snapshots")
		fmt.Println("that need these will stop until it can be read, and say so. To put everything")
		fmt.Println("back in state.json: backup-maker keychain disable")
		printDaemonRestartHint()
		return nil
	},
}

var keychainDisableCmd = &cobra.Command{
	Use:   "disable",
	Short: "Put stored passwords back in state.json",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := refuseWhileDaemonRuns("disable"); err != nil {
			return err
		}
		// The update's load is what reads every secret back out of the keyring, so
		// inside the closure this State either holds them all or knows which it
		// could not get.
		var entries []secretEntry
		alreadyOff := false
		if _, err := config.UpdateState(func(state *config.State) error {
			if !state.SecretsInKeyring {
				alreadyOff = true
				return config.ErrStateUnchanged
			}

			// REFUSED RATHER THAN DONE PARTIALLY. Switching off means writing the
			// real passwords back into the file, and a password that cannot be read
			// out of the keyring cannot be written back. Carrying on would produce a
			// file that says the passwords are in state.json with those ones simply
			// absent, and the keyring entries deleted below — which is the one
			// outcome that loses a password for good.
			if misses := state.KeyringMisses(); len(misses) > 0 {
				return fmt.Errorf(`%d stored password(s) cannot be read out of the OS keyring right now:
  %s

Switching keyring storage off would have to write them back into state.json, and
they are not available to write. Unlock the keyring (or run this from a normal
login session rather than at boot) and try again. Nothing has been changed`,
					len(misses), strings.Join(misses, "\n  "))
			}

			entries = secretEntries(state)
			state.SecretsInKeyring = false
			return nil
		}); err != nil {
			return err
		}
		if alreadyOff {
			fmt.Println("Passwords are already stored in state.json (mode 0600). Nothing to do.")
			return nil
		}

		// The file first, with the real values in it — that is the save above.
		// Only once it has landed is there a second copy to delete: the reverse
		// order would leave a window in which neither storage holds the password.
		var stale []string
		for _, e := range entries {
			if err := config.KeyringForget(e.account); err != nil {
				stale = append(stale, e.account)
			}
		}

		fmt.Printf("Put %d password(s) back in state.json (mode 0600, in your config directory).\n", len(entries))
		if len(stale) > 0 {
			// Worth saying rather than swallowing: the password is safely in the
			// file, but a copy the user did not ask to keep is still in their
			// keyring, and only they can remove it.
			fmt.Println()
			fmt.Println("These keyring entries could not be removed and are now unused copies.")
			fmt.Printf("Delete them from your keyring app if you would rather they were gone (service %q):\n", config.KeyringService)
			for _, account := range stale {
				fmt.Println("  " + account)
			}
		}
		printDaemonRestartHint()
		return nil
	},
}

var keychainStatusCmd = &cobra.Command{
	Use:   "status",
	Short: "Where stored passwords are kept, and whether the keyring answers",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		state, err := config.LoadState()
		if err != nil {
			return err
		}
		entries := secretEntries(state)

		if state.SecretsInKeyring {
			fmt.Println("Keyring storage: ON")
		} else {
			fmt.Println("Keyring storage: OFF — passwords are in state.json (mode 0600)")
		}
		if err := config.KeyringProbe(); err != nil {
			fmt.Println("This machine's keyring: NOT USABLE —", err)
		} else {
			fmt.Println("This machine's keyring: reachable (a test entry was written, read back and removed)")
		}

		misses := state.KeyringMisses()
		switch {
		case state.SecretsInKeyring:
			// Asked of the keyring itself rather than inferred from the file,
			// because "the keyring holds this" is the question, and a password left
			// in the file by a keyring write that failed would otherwise be
			// reported as held.
			var held, inFile []string
			for _, e := range entries {
				if _, err := config.KeyringFetch(e.account); err == nil {
					held = append(held, e.account)
				} else {
					inFile = append(inFile, e.account)
				}
			}
			fmt.Printf("\n%d password(s) in the keyring", len(held))
			if len(inFile) > 0 {
				fmt.Printf(", %d still in state.json (a keyring write failed for these; they are not lost)", len(inFile))
			}
			if len(misses) > 0 {
				fmt.Printf(", %d unreachable", len(misses))
			}
			fmt.Println()
			for _, account := range held {
				fmt.Println("  keyring     " + account)
			}
			for _, account := range inFile {
				fmt.Println("  state.json  " + account)
			}
			for _, account := range misses {
				fmt.Println("  UNREACHABLE " + account)
			}
			if len(misses) > 0 {
				fmt.Println("\nThe destinations and snapshots marked UNREACHABLE cannot be used until the")
				fmt.Println("keyring can be read. Unlock it, or run: backup-maker keychain disable")
			}
		case len(entries) == 0:
			fmt.Println("\nNo passwords are stored on this machine yet.")
		default:
			fmt.Printf("\n%d password(s) in state.json:\n", len(entries))
			for _, e := range entries {
				fmt.Printf("  %-8s %s\n", e.kind, e.name)
			}
			fmt.Println("\nTo move them into the keyring: backup-maker keychain enable")
		}
		return nil
	},
}

// secretEntry is one stored password, with the keyring account it belongs under.
type secretEntry struct {
	kind    string // "share" or "archive", as the user's own word for the thing
	name    string // the destination or snapshot-schedule name
	account string
	secret  string
}

// secretEntries lists every password this State holds, in a stable order.
//
// Sorted because all three subcommands print it, and a list in map order reads
// like a different set of passwords every time it is shown.
func secretEntries(state *config.State) []secretEntry {
	var out []secretEntry
	for name, secret := range state.ShareCredentials {
		out = append(out, secretEntry{"share", name, config.ShareKeyringAccount(name), secret})
	}
	for name, secret := range state.ArchivePasswords {
		out = append(out, secretEntry{"archive", name, config.ArchiveKeyringAccount(name), secret})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].account < out[j].account })
	return out
}

// refuseWhileDaemonRuns stops enable/disable while the daemon is up.
//
// NOT A LOCKING NICETY — IT IS THE DIFFERENCE BETWEEN WORKING AND SILENTLY NOT.
// The daemon holds the whole of state.json in memory and writes it back every
// few seconds as it flushes its counters and sync clocks. Its copy was loaded
// before this command ran, so the very next flush would overwrite the flag this
// command just set, and the file would go back to plaintext (or, switching off,
// back to placeholders pointing at entries this command had already deleted).
// Other state-writing commands do not need this: `set-password` and the
// add-target commands change one entry and re-save config.toml, which makes the
// daemon re-read state.json and adopt it. That merge cannot help here, because
// what changes is how the whole file is written.
func refuseWhileDaemonRuns(verb string) error {
	if _, err := daemon.Connect(); err != nil {
		return nil // not running, or has never run: ours to change
	}
	stop, start := daemonStopStart()
	return fmt.Errorf(`the backup-maker daemon is running, and it would overwrite this change within seconds.

Stop it, make the change, then start it again:
  %s
  backup-maker keychain %s
  %s`, stop, verb, start)
}

// daemonStopStart names the commands for stopping and starting the daemon on this
// OS. Spelled out rather than left as "stop the daemon", because a refusal that
// does not say how to get past it is a dead end — the same reasoning as the
// rename refusals in internal/setup/naming.go.
func daemonStopStart() (stop, start string) {
	switch runtime.GOOS {
	case "linux":
		return "systemctl --user stop backup-maker.service", "systemctl --user start backup-maker.service"
	case "darwin":
		return "launchctl bootout gui/$(id -u)/com.backup-maker.agent",
			"launchctl bootstrap gui/$(id -u) ~/Library/LaunchAgents/com.backup-maker.agent.plist"
	default:
		// Windows autostart is an HKCU Run entry, so there is no service to stop:
		// the daemon is an ordinary process of this user.
		return "(quit the running backup-maker, e.g. from Task Manager)",
			"backup-maker daemon"
	}
}

// printDaemonRestartHint is the counterpart for the case where the daemon was
// NOT running: it will read the new arrangement on its next start, and somebody
// who has just been told the change worked should be told that too.
func printDaemonRestartHint() {
	fmt.Println()
	fmt.Println("The daemon reads this arrangement when it starts, so start it as usual when")
	fmt.Println("you are ready — it was not running, which is why this change was allowed.")
}

func init() {
	// Every CLI command that saves a password can hit a keyring that will not take
	// it. The password is kept either way — it goes back into state.json, which is
	// where it lived before this feature existed — but the user asked for
	// something else to happen, so they are told when it did not. Set here because
	// this is the file that knows about the keyring; it covers set-password, the
	// wizard, adopt and everything else in this package.
	config.KeyringWriteFallback = func(account string, err error) {
		fmt.Println("Note:", account, "could not be put in the OS keyring, so it stays in state.json (mode 0600):", err)
	}

	keychainCmd.AddCommand(keychainEnableCmd, keychainDisableCmd, keychainStatusCmd)
	rootCmd.AddCommand(keychainCmd)
}
