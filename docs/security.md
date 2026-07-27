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
  [Sleeping computers](sleeping-computers.md)).
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

## Credentials & security notes

- Network-drive passwords are stored in `state.json` (file mode 0600, next to
  the config) — never in the shareable `config.toml`. Update them with
  `backup-maker set-password <target>`.
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
- **Alerts are the one thing that can deliberately leave your network** — and
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

## See also

- [Monitoring your backups](monitoring.md) — what the read-only network view
  and the on-destination status page deliberately never show.
- [When a destination fills up](space.md) — exactly what automatic reclamation
  may and may not delete.
- [Reference](reference.md) — where the config and `state.json` live, and how
  the local-only enforcement is implemented.
