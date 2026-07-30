# 4. Timed snapshots

The live mirror follows every change you make, which means it also follows every
mistake. Timed snapshots are the other half: sealed, encrypted copies of a
folder as it stood at one moment, kept on a timer. This page explains what they
are and what the rules around them are.

The wizard calls this kind of backup **Timed**, and the dashboard lists them
under **Timed snapshots** — separately from **Incremental backups**, because
the two make different promises about your files.

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


Alongside the real-time mirror, `backup-maker wizard` sets up **timed full
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

## Changing a schedule after it exists

Every row under **Timed snapshots** on the dashboard carries three controls.
None of them deletes a snapshot that has already been written.

- **Pause** stops the schedule running and nothing else: the password, the
  retention count and the folder it covers all stay exactly as they are, and
  the row reads *paused* rather than looking like a failure. **Resume** puts it
  back. If the interval passed while it was paused, the next run happens at the
  next check rather than waiting a full period.
- **Edit** opens a form on the row itself, with everything about the schedule
  that is safe to change:

  - **How often it runs** — `hourly`, `12h`, `daily` or `weekly`, shown as a list
    with the current setting already selected.
  - **How many snapshots to keep.** Lowering it deletes nothing immediately: the
    extra snapshots are pruned by the next run, so a number typed by mistake can
    be corrected before it costs you anything.
  - **Whether it packs everything**, including `node_modules` and build output.
    This is the setting most worth knowing about, because it is usually the
    difference between a few gigabytes and a great many — and until recently it
    could only be chosen once, in the wizard, and never changed. Turning it off
    does not shrink the zips already written; it applies from the next run.
  - **A new password.** Changing it affects snapshots written from now on. The
    zips already on your destination still open with the password that made them
    — nothing is re-encrypted, and nothing already written becomes unreadable.
    Keep the old password for as long as you keep those.

  It deliberately does **not** offer to change the folder. Re-pointing a schedule
  at different files is exactly how somebody ends up with a snapshot of the wrong
  thing, and it has happened here. That is a new schedule, made on purpose.
- **Stop** ends the schedule for good. It is called *stop* and not *delete*
  because the snapshots it already wrote are left exactly where they are, and
  still open with the password that made them. What is forgotten is
  backup-maker's own stored copy of that password — so if you may ever want to
  open those zips, keep your own copy of it first.

A snapshot whose password backup-maker doesn't hold never runs, and says so in
the row with an **enter password…** button beside it.

## Stopping the folder instead

Stopping a folder is a different thing from stopping a schedule, and it has its
own section on the dashboard — see
[Stopping a folder](2-first-backup.md#stopping-a-folder).

## See also

- [Choosing destinations](3-destinations.md) — why a daily snapshot covers what
  no mirror can, and how to size the drive that holds them.
- [When a destination fills up](../reference/space.md) — how snapshot retention interacts
  with automatic space reclamation.
- [Restoring & recovery](6-restoring.md) — getting files back out again.
