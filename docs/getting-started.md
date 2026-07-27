# Getting started

Everything you need after installing the binary: your first backup, making the
daemon survive reboots, adding destinations, and what actually gets copied.
Read this once, in order, and you're done.

If you haven't installed backup-maker yet, start with
[Install](../README.md#install).

## Quick start

```sh
backup-maker init
backup-maker add-folder ~/code        # what to protect
backup-maker daemon &                 # start the engine
backup-maker autostart enable         # survive reboots
backup-maker web                      # or set it all up in the browser
```

## Which version am I running?

`backup-maker version` prints it, and the dashboard shows it in the footer, so
you can tell whether a machine has a given fix without opening a terminal on
it. A release reads `v0.1.2`; a self-compiled binary reads `dev`, or
`dev-dirty` if the tree had uncommitted changes.

The read-only network view deliberately omits it — see
[Monitoring](monitoring.md#which-version-am-i-running).

## Keeping it running

A backup that stops when you close a terminal is not a backup. Two things make
it permanent, and the order matters.

**Put the binary somewhere it will stay.** `autostart` records the path of the
binary you run it from, so install it before you enable anything:

```sh
install -m 755 backup-maker ~/.local/bin/backup-maker
```

Do not leave it in a downloads folder, in a git checkout you might move, or —
least of all — inside a folder you are backing up. Any of those can vanish
under the service later.

**Then enable it:**

```sh
backup-maker autostart enable
```

On Linux that writes a systemd *user* unit at
`~/.config/systemd/user/backup-maker.service` and starts it immediately.
macOS gets a LaunchAgent; Windows gets a Startup entry. Check it took:

```sh
systemctl --user status backup-maker.service    # Linux: should say "active (running)"
backup-maker status                             # and your folders should be in sync
```

**A normal desktop or laptop needs nothing further.** The service starts when
you log in and stops when you log out — which is what you want, because your
files only change while you are using the machine.

**A headless machine needs one more line.** A server, a NAS, or a Raspberry Pi
you only reach over SSH will have systemd kill the service the moment your SSH
session ends, unless you allow your user to linger:

```sh
sudo loginctl enable-linger $USER
```

That single command is the difference between a machine that backs up
continuously and one that backs up only while you happen to be logged in. It
is not optional on anything headless.

To stop it starting automatically: `backup-maker autostart disable`.

### Run `autostart enable` again after every upgrade

The service definition is written **once**, by that command, and nothing else
ever rewrites it. So when a new version of backup-maker adds something that
lives in the service rather than in the program — the restart policy, or the
watchdog that catches a daemon that has locked up — replacing the binary is not
enough. The new protections sit on disk doing nothing, the daemon looks
perfectly healthy, and you find out only on the day something goes wrong and
nothing recovers.

Re-running it is safe and takes a second:

```sh
backup-maker autostart enable
```

backup-maker checks this for you: if the installed service definition is older
than the version you are running, `backup-maker status` and `backup-maker
autostart status` both say so, and the daemon logs a warning when it starts.
It will not rewrite the file behind your back — a program that quietly edits
your system configuration is a worse problem than the one being solved.

## Adding backup targets

Then add backup targets — as many as you like, mixed freely:

**A drive in/on this computer** (SD card, USB stick, external disk):

```sh
backup-maker add-target drive /media/you/SDCARD
```

**A network drive** — a NAS, a router's USB drive, or a folder shared by any
Windows/Mac/Linux machine. Nothing is installed on the other machine:

```sh
backup-maker scan                                     # find drives on your network
backup-maker add-target share //192.168.1.1/usb1      # open/guest shares
backup-maker add-target share //NAS/backups --user bob # password-protected shares
```

**Optional: a second computer also running backup-maker.** Only worth it if you
want the strongest transfer mode (block-level verified delta sync). This is the
one case that needs backup-maker installed on both ends:

On the receiving machine, enable it first:

```sh
backup-maker receive enable --root D:\Backups
```

Then either use the dashboard (the wizard has an "Add a computer by device ID"
section) or the CLI:

```sh
# on the receiving machine:
backup-maker pair                       # shows its device ID and a QR code
# on the sending machine (browser or CLI):
backup-maker add-target device <THAT-DEVICE-ID>
# back on the receiving machine (IDs are unforgeable — compare prefixes):
backup-maker pair accept <THIS-DEVICE-ID>
```

The receiving machine's dashboard shows pending approvals as "Approve" buttons;
you can approve there instead of the CLI.

`backup-maker status` shows live health for every folder × destination;
`backup-maker web` opens the dashboard, where the setup wizard walks
you through all of the above.

Both also keep a running total of how much has been copied since you installed
it — `Backed up in total: 1.5TB across 82,391 files since 3 March 2026`. It
counts what backup-maker writes itself, to drives and network shares. Copies
sent to a paired computer travel through the sync engine rather than through
that code, so they are left out and the line says so, rather than quietly
reporting a smaller number as if it were the whole story.

## What gets backed up

Everything in the folders you add, except common junk (`node_modules`,
`__pycache__`, build dirs, caches — see `config.toml`). `.git` **is** backed
up: losing repositories is unacceptable. Override per folder with
`--ignore`/`--no-default-ignores`, or edit a folder's exclude patterns from the
dashboard. Changes take effect within a few seconds; files that start matching
stop being copied, and the copies already on your destinations are moved into
version history rather than deleted.

**One exception you don't control:** backup-maker's own configuration folder
(`~/.config/backup-maker`, `~/Library/Application Support/backup-maker`,
`%AppData%\backup-maker`) is never copied to a destination, in any kind of
backup, even with `--no-default-ignores`. It holds your share and snapshot
passwords and this machine's sync identity — copying it would put the key to
your encrypted snapshots on the same disk as the snapshots. Only that exact
folder is skipped: a directory of your own that merely happens to be *named*
`backup-maker` is backed up like anything else.

## See also

- [Choosing destinations](destinations.md) — the arrangement worth copying, and
  how to size it.
- [Scheduled snapshots](snapshots.md) — encrypted point-in-time copies alongside
  the live mirror.
- [Sleeping computers](sleeping-computers.md) — read this if your backup drive
  is plugged into a PC that sleeps.
- [Monitoring](monitoring.md) — checking backups from a phone, or while this
  computer is off.
