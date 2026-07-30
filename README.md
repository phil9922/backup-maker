<p align="center">
  <picture>
    <source media="(prefers-color-scheme: dark)" srcset="img/header-dark.png">
    <img src="img/header-light.png" width="100%" alt="backup-maker — real-time, versioned backups to any drive you can reach">
  </picture>
</p>

# backup-maker

**Install it on the one computer you want protected.** Everything else is just
somewhere to put the copies — no software to install over there, no accounts,
nothing leaving your network.

Every folder you choose is mirrored, in real time, onto as many places as you
like at once:

- **a drive inside or plugged into that computer** — an SD card, a USB stick,
  an external disk
- **a USB drive plugged into your router**
- **a NAS**
- **a shared folder on any Windows, Mac or Linux machine** on your network —
  if it can share a folder, it can hold your backups, with **nothing installed
  on it**

That's the whole model: one install, many destinations.

> **Optionally**, a second computer can also run backup-maker and pair with the
> first. That unlocks the strongest transfer mode (block-level, verified, delta
> sync) and works between any operating systems — but it is an upgrade, not the
> point. You never need it.

### Fully self-hosted, and local by design

Your backups go to hardware you own, over your own network. There is **no cloud
service, no account, no telemetry, and no off-site mode** — not disabled by
default, but absent. The sync engine is locked to your local network: public
discovery, relays and NAT traversal are all switched off with no setting to
turn them back on, so your machine is never announced to anyone. The dashboard
listens on `127.0.0.1` only. Network scanning runs solely when you click the
button.

The one time anything is downloaded is if you pair a second machine: a pinned,
checksum-verified [Syncthing](https://syncthing.net) build, fetched once.
Backing up to drives, routers, NAS boxes and shared folders never touches the
internet at all.

**Update checking is off by default.** backup-maker can ask github.com once a
day whether a newer release exists and tell you when there is one. It is the
only part of the program that would contact an outside service on its own, so it
stays off until you switch it on — and it **only ever tells you**: nothing is
downloaded, replaced or run, and you choose when to upgrade. The request carries
a User-Agent and nothing else; no machine name, no version, no identifier.

**Two other things can leave your network, and only if you switch them on.** Alerts
can be delivered to a webhook address of your choosing, or pushed to your phone
through [ntfy](https://ntfy.sh) — including the public ntfy.sh server if that is
the one you pick. Both are **off by default and never required**: backups work
identically without them, and nothing is sent anywhere until you enter an
address. Your backed-up *data* is never involved either way — only a short
message saying something needs attention. If you would rather the receiving
server learn nothing at all, "Don't include any detail" reduces that message to
a severity and a fixed sentence, with no machine, folder or destination names.
See [5. Monitoring your backups](docs/guide/5-monitoring.md).

For a copy that survives your house, use a drive you rotate somewhere else —
see [Hardware guidance](docs/setup/hardware.md).

> **Project snapshot** (for anyone — human or AI — getting oriented): a
> working Go application; one self-contained binary per OS (Linux, Windows,
> macOS) carrying the CLI and the localhost web dashboard. Core sync paths for
> all three destination types are implemented and end-to-end tested. See
> "Status & roadmap" below for what's not built yet, and the
> [documentation](docs/README.md) for everything else. Author: Phil
> Kokoska.

- **Real-time & incremental** — a saved file is on every destination within
  seconds; only changed files transfer, never the whole tree.
- **One-way with history** — destinations are mirrors of the source and keep
  ~30 days of old file versions, so deleting or corrupting a file on your
  computer can't silently destroy the backup.
- **Any OS, zero runtime dependencies** — one self-contained binary each for
  Linux, Windows, and macOS. Network drives are reached with a built-in SMB
  client: no mounting, no admin rights.
- **Private by default** — no accounts, no cloud, no telemetry. Machine-to-
  machine pairing (if you use it) is mutually-authenticated TLS. Network
  scanning runs **only when you ask** — never in the background.

## What it looks like

Install, start the daemon, and open the dashboard — it walks you through your
first backup. Everything below runs on `127.0.0.1`; nothing is published
anywhere.

> The folders, drives and computers in these screenshots are examples, not real
> machines.

### Setting up a backup

The wizard is how backups are made — every one of them, not just the first.
Each run sets up a single backup, and each step asks one question.

**1. What kind of backup is this?** The two kinds protect against different
things, so this is asked first: it changes what the rest of the wizard needs
to know.

![Wizard step 1: three options. "Incremental — only changed files are copied, continuously, within seconds of you saving; the destination holds a normal browsable copy plus about 30 days of previous versions.", "Timed — a full encrypted snapshot on a schedule; everything packed into one password-protected zip per run, nothing copied in between.", and "Restore this machine — I already have backups on a drive or network share; rebuild this computer's setup from them." Each says what it is best for.](docs/screenshots/01-wizard-kind.png)

- **Incremental** — a live copy. Save a file and it's on the destination within
  seconds. What lands there is an ordinary, browsable copy of your files, plus
  roughly 30 days of previous versions. This is the one that saves you from a
  deleted file, a bad edit, or a dead drive.
- **Timed** — sealed snapshots. On your schedule, everything is packed into one
  AES-256 encrypted zip, and nothing is copied in between. This is the one that
  lets you go back to exactly how things were on a given day, or hand someone a
  sealed copy to keep off-site.

You can have both: run the wizard twice.

**2. Which folder should be protected?** Click through your folders, or type a
path. Whatever folder you're looking at can be protected from the path bar, so
you can navigate into the thing you want and choose it there.

![Wizard step 2: a folder picker showing /home/alex/Desktop in a path bar with a "Protect this folder" button beside it, the sub-folders Development and notes listed below each with their own "Protect this" button, and a box to type a path](docs/screenshots/02-wizard-folder.png)

**3. Where should the copies go?** You get a list of *computers* — this one,
and any on your network sharing storage. Click one to see what it offers, and
tick as many destinations as you like, across as many machines as you like.

![Wizard step 3: a list of computers — my-laptop, NAS, STUDIO-PC, ROUTER. This computer is expanded showing an SD card and a backup SSD, both ticked, with free space beside each; the NAS is expanded showing two shares with one ticked, and explains that backup-maker is not running there so it can only offer folders that computer already shares. STUDIO-PC is marked as needing a password. "3 destinations chosen" beneath](docs/screenshots/03-wizard-destinations.png)

A locked computer asks for credentials right there — nothing to install on it.
And a second internal disk, or any folder at all, works just as well: "Or
choose any folder on this computer".

**4. Check it over and start.** Nothing is written until you confirm, and if
any destination can't be reached, *nothing* is saved — you never end up half
protected while believing otherwise.

![Wizard step 4: a review listing the kind of backup, the folder, and "Copies kept on (3)" naming my-laptop to SDCARD, my-laptop to BackupSSD, and NAS to backups, above a "Start backing up" button and a note that nothing has been saved yet](docs/screenshots/04-wizard-review.png)

**Timed backups get one extra step** — how often, the password, and whether to
**include everything**. Snapshots normally skip the same junk as the mirror;
ticking that option seals `node_modules` and build output into the archive while
the live mirror stays lean. There is no recovery path if the password is lost,
and the wizard says so rather than burying it.

![An extra wizard step for timed backups: a schedule dropdown set to every week, how many snapshots to keep, an "include everything, even node_modules" option that is off by default, password and repeat-password fields, and a red warning that there is no way to recover the password](docs/screenshots/05-wizard-schedule.png)

### Adding a second backup to the same folder

A folder can have more than one kind of backup. Run the wizard again and the
folder step offers what you're already protecting:

![The wizard's folder step, offering folders that are already protected with an "Add a backup for this" button beside each](docs/screenshots/06-wizard-existing-folder.png)

So a folder mirrored continuously to an SD card can also get a daily snapshot on
a different drive, without being set up twice.

### Watching it work

The dashboard shows every folder against every destination, and keeps the two
kinds of backup apart: **Incremental backups** and **Timed snapshots** are
separate panels, so you can see at a glance which promise a folder actually
has. Folders you've stopped move to **No longer protected**, where the copies
they left behind are still named.

![Dashboard with a green "Everything is backed up" verdict and the address another device can watch from beneath it, an "Incremental backups" table grouping code, documents and photos under their paths with every destination "backed up" in green, a "Timed snapshots" table naming the folder each schedule covers with Pause, Edit and Stop beside it, a "Backup drives" panel listing each destination with its location, health and free space — the SD card also naming which physical card it is, and each with a Rename button — and a "No longer protected" panel showing a stopped folder whose copies are still on two destinations](docs/screenshots/07-dashboard.png)

While files are moving you get live progress per destination — real byte
counts, updating in place. Nothing needs refreshing.

![Dashboard mid-transfer: a "Backing up now" verdict, progress bars part way across with labels like "2.1GB of 5.2GB", one destination still "checking for changes: 18,422 of 72,510", a folder on its first backup labelled "working out what to copy", and timestamps counting in seconds](docs/screenshots/08-transferring.png)

Each destination also shows how full it is — a usage bar and "312GB free of
1.8TB", turning amber as it tightens and red once it crosses the headroom you've
reserved (see [When a destination fills up](docs/reference/space.md)). A destination
that's asleep or unreachable keeps its last reading, marked "as of 2h ago",
rather than the bar vanishing. Free space is read once a minute, so watching the
dashboard never hammers a network drive.

When something is wrong it says so plainly: a destination that's offline, one
unreachable long enough to go stale, waiting to pair (connected but not yet
approved), or another machine asking for approval.

![Dashboard showing a network drive offline in red, a paired computer stale for 9 days marked with a warning triangle, a snapshot schedule that failed, the offline drive keeping its last free-space reading marked "as of 4h ago", and a notice that a machine called attic-pi wants to back up here](docs/screenshots/09-offline.png)

There's more — a read-only view for your phone, a status page that works while
this computer is off, and the small-screen layout — in
[Monitoring your backups](docs/guide/5-monitoring.md).

## Install

Download the archive for your OS and CPU from the [latest release][releases],
unpack it, and put the `backup-maker` binary somewhere on your `PATH`. It's a
single self-contained file — no runtime to install, no service to register, and
still nothing to install on the destinations.

```sh
tar xzf backup-maker_0.1.0_linux_amd64.tar.gz
sudo install backup-maker /usr/local/bin/
backup-maker version
```

On Windows, unzip the archive and run `backup-maker.exe` from wherever you keep
it. On macOS the binaries aren't code-signed, so the first run is blocked until
you either allow it under System Settings → Privacy & Security, or clear the
quarantine flag yourself:

```sh
xattr -d com.apple.quarantine ./backup-maker
```

Every release ships a `checksums.txt`. To check a download arrived intact:

```sh
sha256sum -c checksums.txt --ignore-missing
```

Prefer to compile it? See
[Building from source](docs/reference/building.md#building-from-source).

[releases]: https://github.com/phil9922/backup-maker/releases/latest

## Quick start

```sh
backup-maker init
backup-maker add-folder ~/code        # what to protect
backup-maker daemon &                 # start the engine
backup-maker autostart enable         # survive reboots
backup-maker web                      # or set it all up in the browser
```

That gets a folder protected. [Getting started](docs/guide/1-install.md) covers
the rest: adding drives, network shares and paired machines, making the daemon
survive a logout as well as a reboot, and what does and doesn't get copied.

## Documentation

The full set lives in [docs/](docs/README.md), in roughly the order you'll need
it. It is also built into the binary: click **Docs** in the dashboard, or run
`backup-maker docs --export ./manual` to write it out as web pages you can read
with no daemon, no web server and no internet.

**Getting started**

- [Getting started](docs/guide/1-install.md) — your first backup, keeping the
  daemon running, adding destinations, and what gets copied.

**Destinations**

- [Choosing destinations](docs/guide/3-destinations.md) — the arrangement that covers
  the most failure modes for the least money, and how to size it.
- [Preparing a new drive](docs/guide/preparing-a-drive.md) — a drive out of the
  box has no filesystem and cannot hold backups until it has one. Formatting
  and mounting it, including readying one for a Pi on a different computer.
- [Hardware guidance](docs/setup/hardware.md) — categories and principles
  rather than brands: always-on targets, offsite options, drives, formats.
- [Setting up a Raspberry Pi as a backup target](docs/setup/raspberry-pi.md) —
  from an empty microSD card to a Pi that receives backups around the clock.

**Using it**

- [Timed snapshots](docs/guide/4-snapshots.md) — encrypted point-in-time copies on
  a timer, alongside the live mirror, and how to pause or change a schedule.
- [Monitoring your backups](docs/guide/5-monitoring.md) — desktop alerts when backups
  stop working, the read-only network view, the status page written onto each
  destination, and the phone layout.
- [When a destination fills up](docs/reference/space.md) — what gets deleted to make
  room, what never does, and how to turn it on.
- [Sleeping computers](docs/setup/sleeping-computers.md) — why a sleeping destination
  silently stops receiving backups, and the three fixes including Wake-on-LAN.

**When things go wrong**

- [Restoring & recovery](docs/guide/6-restoring.md) — getting a file back, rebuilding a
  machine from a destination, and repairing a backup someone edited.
- [Security & safety properties](docs/reference/security.md) — the guarantees, where
  credentials live, and why there is no off-site mode.

**Reference**

- [Reference](docs/reference/building.md) — config file locations, the daemon's
  architecture, building from source, and cutting a release.

## Status & roadmap

Implemented and E2E-tested: all three target types (local drive, SMB network
drive, paired machine), real-time sync, versioning, offline/catch-up,
wrong-drive refusal, read-back verification, on-demand network scan,
scheduled encrypted archive snapshots (CLI wizard and browser), automatic
space reclamation on full destinations, adopting an existing destination on a
new machine from its on-disk manifest (browser wizard and CLI), Wake-on-LAN
for sleeping targets, the browser setup wizard with folder picker and
machine/storage browsing, live per-destination transfer progress, folder and
destination management from the dashboard, a Settings panel (which alerts you
get and how they reach you), desktop notifications, webhook and ntfy delivery, a
read-only network view for other devices — open to the whole network or to
devices you approve one at a time — a status page written to each destination,
autostart, unit/conformance tests
(`go test ./...`; SMB suite runs against a real server via `BM_SMB_TEST_URL`),
plus an end-to-end browser test that drives the real dashboard against a real
daemon.

Not yet built: Windows firewall helper command, OS-keychain credential storage.

## Support

backup-maker is free and open source, built and maintained by one person. If
it saved you a hard drive, you can
[buy me a coffee](https://ko-fi.com/phil9922). ☕

## License

MIT License — see [LICENSE](LICENSE). Third-party components and their
licenses are listed in [THIRD-PARTY-NOTICES.md](THIRD-PARTY-NOTICES.md); the
Syncthing engine (MPL-2.0) is downloaded as a separate, unmodified official
binary at first run.
