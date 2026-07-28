# 2. Your first backup

What to protect, where the copies go, and what actually gets copied. Assumes
backup-maker is installed and running — if not, start with
**[1. Installing backup-maker](1-install.md)**.

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

## Stopping a folder

**Stop protecting** on a folder's card stops future copying. It deletes
nothing: the copies already on your destinations stay exactly where they are.

Because those copies outlive the folder that made them, the folder does not
simply disappear from the dashboard — it moves into a section called **No
longer protected**, listing where its backups still sit. Otherwise 1.4GB on a
drive would be named by nothing at all.

Each stopped folder there offers three things:

- **Turn back on** — start backing it up again. It reconnects to the
  destinations that are still set up and carries on from the copy already
  there; nothing is copied again from scratch. If none of its old destinations
  still exists here, it says so rather than offering a button that would fail.
- **Forget this** — remove the reminder only. The backups are left where they
  are; the page just stops mentioning them. Use this for a drive you no longer
  own.
- **Delete backups…** — the one action in backup-maker that deletes a backup on
  purpose. It removes the mirrored copies **and their saved previous versions**
  from each destination it can reach, and it asks twice: a confirmation listing
  every path it will delete, then the folder's own name typed back exactly. The
  daemon checks that typed name again on its side.

Three things **Delete backups…** never touches:

- **The folder on this computer.** backup-maker is one-way; a source folder is
  read from and never written to or deleted from. That rule has no exception.
- **Copies on another computer.** A paired machine manages its own storage, so
  the deletion has to be done over there. The card says which destination that
  applies to.
- **Timed snapshots.** A snapshot is a single encrypted zip that may hold other
  folders too, so there is no way to remove just this folder from one. Old
  snapshots go when their retention count prunes them — see
  [4. Timed snapshots](4-snapshots.md).

Removing a *destination* or a *schedule* behaves the same way: it changes what
happens next, and never deletes a backup already written.

---

Next: **[3. Choosing destinations](3-destinations.md)** — which arrangement of
drives and machines actually protects you.
