# Security & safety properties

What backup-maker guarantees about your data, and where your credentials live.
Worth reading once before you trust it with anything, and again if you're
wondering what it will and won't do on its own.

## Safety properties

- Every target is stamped with a marker file; if *different* storage shows up
  at the same location, backup-maker refuses to touch it.
- Files written to network drives are **read back and checksum-verified** by
  default (SMB has no end-to-end integrity checking of its own; disable per
  target with `--no-verify`).
- A drive being unplugged, a NAS powering off, or wifi dropping pauses that
  target cleanly; on return it catches up exactly, without recopying
  everything. A target with a MAC address configured is also sent a
  Wake-on-LAN packet while it's offline (best-effort — see
  [Sleeping computers](../setup/sleeping-computers.md)).
- Writes are atomic (temp file + rename) — a power cut or connection drop
  mid-sync never leaves a half-written file visible.
- Targets that don't preserve file timestamps (some router/NAS firmware) are
  detected automatically and compared by size + recency instead.
- Changes made *on a target* are never synced back to the source.
- backup-maker's own configuration folder — share passwords, snapshot
  passwords, this machine's sync identity — is never copied to any destination,
  whatever folder it sits inside. If an earlier version already copied it, the
  logs say so once per destination and name the path: delete it there yourself
  (nothing is ever deleted from a destination on your behalf) and change the
  passwords it held.
- A target unseen for 7 days shows a stale warning (`!!`) in status.
- **A destination describes itself and only names the others.** Each machine
  leaves a manifest (`<machine>/.backup-maker-manifest.json`) so the machine can
  be rebuilt from the storage alone. It holds no password, and since v0.1.15 it
  holds the address, username and marker id of **that destination only** — your
  other destinations appear as a name and a type. A drive can be lost, stolen or
  resold, and one carrying every share address, SMB username, MAC and paired
  device id in the house is a map of it. It does carry this machine's source
  folder paths, which include your username on Linux and macOS: they describe
  the same machine whose files are already on the drive in the clear, and adopt
  needs them to put folders back where they were. See
  [Restoring](../guide/6-restoring.md).
- **The manual on a destination discloses nothing.** Each machine also leaves a
  copy of the documentation (`<machine>/.backup-maker-manual/`, ~2MB, rewritten
  only when the version changes) so a drive can be understood without the
  computer that wrote it. It is byte-for-byte the same pages this build serves
  and publishes — no configuration, no paths, no names. It is still written only
  where anything of ours may be written: storage whose identity marker is not
  recognised gets no manual either.

## Credentials & security notes

- Network-drive passwords are stored in `state.json` (file mode 0600, next to
  the config) — never in the shareable `config.toml`. Update them with
  `backup-maker set-password <target>`. They can optionally be moved into your
  OS keyring instead — see [below](#optional-keeping-passwords-in-the-os-keyring).
- The same applies to **alert delivery credentials**: the webhook address, the
  ntfy topic and the ntfy access token all live in `state.json` rather than
  `config.toml`. A webhook URL is usually a right to post in its own right (a
  Slack or Discord endpoint), and on ntfy.sh **the topic name is the whole of
  the access control** — anyone who learns it can subscribe to your alerts. For
  the same reason none of them is ever sent back to the dashboard: it is told
  only that one is saved, so replacing one means typing it again. Set them in
  the dashboard's Settings panel, not by hand.
- The built-in SMB client speaks SMB 2/3 only. Devices that offer nothing but
  SMB1 (very old routers/NAS) aren't supported — check for a firmware update.
- Automatic deletion to reclaim space is **off unless you set `min_free_gb`**,
  and even then only ever removes old versions and old snapshots — never the
  live copy, and never a snapshot job's last archive.
- Wake-on-LAN is **opt-in per target** — no packet is ever sent unless you
  give that target a MAC address. Magic packets are local-network broadcasts
  that carry nothing but the target's own MAC (no data, no credentials), so
  they stay within `lan-only` mode. They are only sent while that target is
  offline, at most once every 5 minutes.
- **There is no off-site mode for your backups.** The sync engine is pinned to
  your local network: public discovery, relays and NAT traversal are switched
  off in code, with no setting to re-enable them, so your files are never
  announced or sent to any outside service. For a copy that survives the
  building, rotate a drive to another location.
- **Update checking asks github.com, and is off by default.** When switched on
  it makes one request a day to a public GitHub endpoint and reads back a
  version number. The request carries a User-Agent and nothing else — no
  machine name, no version, no install identifier, no query string — so what
  GitHub learns is that some address asked a public repository a public
  question. **Nothing is ever downloaded, replaced or run**: the feature tells
  you a release exists and stops there, deliberately, because an updater that
  installs by itself can stop backups on every machine at once.
- **Alerts are the other thing that can deliberately leave your network** — and
  only if you switch a delivery method on. A webhook posts to an address you
  choose; ntfy publishes to a topic on a server you choose, which may be the
  public ntfy.sh. Both are **off by default and never required for backups to
  work**, and nothing is sent until you enter an address. What travels is a
  short alert, never backed-up data.

  Worth understanding rather than skipping: the last hop to a phone always
  crosses somebody else's server, and *"backups to nas-attic have been stale for
  3 days"* describes your household to whoever runs it. **"Don't include any
  detail"** reduces the message to a severity, a timestamp and the fixed
  sentence *"backup-maker needs attention"* — no machine name, no folder or
  destination names. Your phone still buzzes. On ntfy specifically, remember a
  topic name is not a password: pick one with real randomness in it, or protect
  the topic and give backup-maker the access token.

## Optional: keeping passwords in the OS keyring

Two of the passwords above can be moved out of `state.json` and into your
operating system's keyring — GNOME Keyring or KWallet on Linux, the Keychain on
macOS, the Credential Manager on Windows:

- each **network drive's** login password, and
- each **scheduled snapshot's** zip password.

Nothing else moves. The IPC token, the sync engine's API key, the webhook
address and the ntfy token stay in `state.json`, because they are needed on
every start — including the starts where no keyring answers.

```sh
backup-maker keychain status     # where the passwords are, and whether the keyring works here
backup-maker keychain enable     # move them into the keyring
backup-maker keychain disable    # put them back in state.json
```

**It is optional and it is off unless you turn it on.** `state.json` is mode
0600 in your own config directory, so on a single-user machine the keyring's
gain is narrower than it sounds: encryption at rest for a file that only your
account can read anyway. What it costs is availability, which is the thing a
backup tool cannot trade away.

**The caveat that matters, especially on a Raspberry Pi.** A headless machine
usually has no keyring at all, and a daemon started at *boot* rather than at
*login* has no keyring session even where one is installed — so the passwords
cannot be read, and the destinations and snapshots that need them stop working
until they can be. `keychain enable` tests your machine for real before it moves
anything and refuses if that test fails, so you cannot walk into this by
accident. If it happens later — you locked the keyring, or changed how the
daemon starts — the daemon says so at startup by name, and `keychain disable`
puts everything back.

Two details worth knowing. The **names** stay in `state.json` with a placeholder
where each value was, because a keyring cannot be asked to list what it holds;
that list is the only record of which passwords exist. And if the keyring
refuses a *write*, the password is written to `state.json` as before rather than
being lost, and you are told that it happened.

Stop the daemon before running `enable` or `disable`: it holds the state file in
memory and would write its own copy back over the change. The command refuses
while it is running and tells you the commands for your system.

## Preparing a drive

backup-maker runs as an ordinary user and never gains privilege on its own.
Partitioning a disk needs root, so the one operation that requires it —
formatting a blank drive — is a separate subcommand, and the daemon reaches it
only through `sudo`. If `sudo` will not run it without a password, nothing
tries to work around that: the dashboard shows you the command to run yourself.

Granting the permission is opt-in, one command, and it prints the rule before
installing it:

```sh
sudo backup-maker prepare-drive --install-sudoers
```

It writes one line to `/etc/sudoers.d/backup-maker`:

```
you ALL=(root) NOPASSWD: /usr/local/bin/backup-maker prepare-drive --from-stdin
```

**The rule has no wildcard, and that matters.** It names one exact command that
takes no arguments; which drive to prepare arrives on standard input instead. A
sudoers wildcard matches any further arguments, whitespace included, so an
earlier version of this rule ended in `*` and also granted a passwordless
`prepare-drive --force …` — the flag that skips the "there is already something
on it" refusal, which is the check almost everything else here rests on. There
is now nothing to inject: `--force` has no way through that door at all and
remains available only to someone typing their own password at a terminal.

**`--install-sudoers` refuses to name a binary you can overwrite.** Every check
below lives *inside* this program, so granting passwordless root to a file a
non-root user can replace would not grant "the right to prepare a blank drive",
it would grant root outright — swap the file, run the command, be root. So the
binary and every directory above it must be owned by root and not writable by
group or others. If yours is in `~/.local/bin`, it will tell you to install it
somewhere only root can write and run it again from there:

```sh
sudo install -o root -g root -m 755 ~/.local/bin/backup-maker /usr/local/bin/backup-maker
sudo /usr/local/bin/backup-maker prepare-drive --install-sudoers
```

A sudoers rule still cannot express "only a disk with nothing on it". What
actually protects you, all of it re-checked as root at the moment of acting and
none of it decided in the browser:

- the device must be a whole disk named exactly, not a partition, not a symlink,
  not a `/dev/disk/by-*` alias
- nothing may be mounted from it and it may not be in use as swap — which is
  what puts the system disk permanently out of reach
- `wipefs` must find **no** signature of any kind: a drive with a filesystem or
  partition table on it is refused outright, and the flag that overrides this
  is not reachable from the dashboard
- no folder this machine backs up may live on the disk, and the mount point may
  not overlap one
- the typed confirmation must match a phrase derived from the drive itself

The rule names one absolute path. **Move** the binary and the rule stops
matching. **Replacing** the file at that path is a different matter — whatever
sits there is what runs as root, which is precisely why the path has to be one
only root can write to, and why `--install-sudoers` checks that before it will
write the rule.

Consequences worth weighing before you install it:

- Anyone who can reach the dashboard **and** hold its token can format a blank
  drive attached to that machine. The dashboard is bound to localhost and needs
  a token from `state.json` (mode 0600), so this is the same trust boundary as
  the rest of the API — but it is the only part of that API that reaches root.
- The LAN network view is read-only and cannot trigger it.
- It grants nothing else. It is not a general `NOPASSWD` and does not let the
  dashboard run other commands as root.

Remove it whenever you like; nothing else stops working:

```sh
sudo rm /etc/sudoers.d/backup-maker
```

Without it, drive preparation still works — you run the printed command in a
terminal instead.

## See also

- [My drive doesn't show up](../guide/troubleshooting-drives.md) — the whole
  drive-setup flow, including what to run when you would rather not grant the
  permission above.
- [Monitoring your backups](../guide/5-monitoring.md) — what the read-only network view
  and the on-destination status page deliberately never show.
- [When a destination fills up](space.md) — exactly what automatic reclamation
  may and may not delete.
- [Reference](building.md) — where the config and `state.json` live, and how
  the local-only enforcement is implemented.
