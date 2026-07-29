# 1. Installing backup-maker

Getting the binary onto a machine and making it survive a reboot. Ten minutes,
once per computer.

If you have not downloaded it yet, the archives are on the
[releases page](https://github.com/phil9922/backup-maker/releases/latest) —
unpack it and put the `backup-maker` binary somewhere on your `PATH`. There is
nothing else to install, and **nothing to install on the destinations** — a
USB drive, a NAS, a router's shared disk and a Samba box all work untouched.

One exception, and it is about setting up rather than backing up: a *blank*
drive plugged into another computer has to be partitioned and mounted on that
computer before anything can be shared from it. If you want to do that from a
browser rather than a terminal, that machine needs backup-maker too — see
[Troubleshooting: my drive doesn't show up](troubleshooting-drives.md).

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
[Monitoring](5-monitoring.md#which-version-am-i-running).

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

---

Next: **[2. Your first backup](2-first-backup.md)** — choosing what to protect
and where the copies go.
