# Scheduled snapshots (encrypted point-in-time copies)

The live mirror follows every change you make, which means it also follows every
mistake. Scheduled snapshots are the other half: sealed, encrypted copies of a
folder as it stood at one moment, kept on a timer. This page explains what they
are and what the rules around them are.

> **"Snapshot" means complete-at-a-moment, not everything on disk.** It's a
> whole copy of the folders you chose, frozen at one instant — as opposed to
> the mirror, which follows every change. By default it honours the same
> exclude list, so `node_modules` and build output are skipped in snapshots
> just as they are in the mirror.
>
> A schedule can opt out of that with **"include everything"**, which seals the
> excluded junk into the snapshot while leaving the live mirror lean. That's
> the combination worth knowing about: a small SD card mirroring only your
> source, and a bigger drive holding a complete sealed archive you could still
> restore years later, when a dependency may no longer be downloadable.


Alongside the real-time mirror, `backup-maker wizard` sets up **scheduled full
backups**: AES-256 password-protected zip snapshots of chosen folders, written
to any drive or network-drive target on a timer (hourly/daily/weekly or a
custom interval), with a retention count so old snapshots prune automatically.
The wizard also lets you select/deselect folders and exclude files or
subfolders within them.

- A password is **required** — backup-maker refuses to write an unprotected
  archive. It's stored only in the private `state.json` on your machine; if
  you lose it, the archives cannot be opened.
- The password protects the **contents, not the file list**. Every zip records
  its entry names and sizes in an unencrypted index, so anyone holding a
  snapshot can see which files you backed up and how big they are, without
  being able to read a single one of them. That is how the zip format works,
  not a setting backup-maker leaves off — no AES-zip tool behaves differently.
  If the names themselves are sensitive, treat a snapshot as something to keep
  on storage you control rather than anywhere public.
- Every archive is re-read and fully decrypted after writing, before it
  counts as done — proof it's restorable.
- Open archives with 7-Zip, WinRAR, Keka, or any AES-capable zip tool
  (Windows Explorer's built-in viewer can't read AES encryption).
- Missed schedules (machine asleep/off) catch up when the daemon next runs.

## See also

- [Choosing destinations](destinations.md) — why a daily snapshot covers what
  no mirror can, and how to size the drive that holds them.
- [When a destination fills up](space.md) — how snapshot retention interacts
  with automatic space reclamation.
- [Restoring & recovery](recovery.md) — getting files back out again.
