# 3. Choosing destinations

Where should the copies actually go? This page describes one arrangement that
covers the most failure modes for the least money, and how to size the storage
it needs. Read it before you buy anything.

## A setup worth copying

If you're not sure how to arrange things, this is the shape that covers the most
failure modes for the least money — and it's what the author runs.

**One computer to protect, two places to put the copies:**

| | Where | Kind | Protects against |
| --- | --- | --- | --- |
| 1 | A card or small drive left in the computer | incremental | *"I broke this an hour ago"* — instant, no network |
| 2 | An always-on box (a Raspberry Pi, a NAS) | incremental | The computer being lost, stolen, dropped or drowned |
| 3 | The same always-on box | timed, daily | *"Put it back how it was last Tuesday"* |

Tasks 1 and 2 are **one wizard run** with two destinations ticked. Task 3 is a
second run against the same folder, choosing *Timed*.

**Why all three.** A card inside the computer is the fastest to recover from and
the first to be stolen along with it. An always-on box on the far side of the
room survives that, and — because it stays powered — it's the one that can hold
a [status page](5-monitoring.md#checking-backups-when-your-computer-is-off) you
can read when the computer is off. And the daily snapshot covers what neither
mirror does: a mistake you don't notice for a week, by which time both mirrors
have faithfully copied it.

**On the always-on box**, either arrangement works and backup-maker only ever
installs on the computer being protected:

- **Share a folder from it** (Samba on a Pi, or a NAS's built-in sharing) and
  add it as a network drive. Nothing else to install.
- **Or run backup-maker on it too** and pair the two machines, for block-level
  verified transfers. Stronger, at the cost of a second install to maintain.

**Sizing it.** Measure before you buy: excluded junk is usually most of a
folder's size. A 21GB development folder was **1.3GB** once `node_modules`,
build output and caches were skipped — so thirty daily snapshots came to ~18GB,
not 600GB. Work out `size × snapshots kept` rather than guessing.

**One thing not to skip:** a second location. Everything above lives in one
building. A drive you occasionally carry elsewhere is what survives a fire or a
burglary — see [hardware.md](../setup/hardware.md).

## Starting with a drive that has nothing on it

A drive out of its box has no partitions and no filesystem. Plugging it in is
not enough for any operating system to use it, so backup-maker cannot offer it
as a destination — but it does now say so, by name, instead of reporting that
nothing is plugged in.

In the wizard the drive appears greyed out under the computer it is attached
to, with the reason beside it and a *Set this drive up…* panel that erases it,
formats it ext4 and mounts it permanently. The panel shows the command it will
run before it runs it, and will not act until you have typed the drive's own
size back — the check that catches the wrong drive.

Two limits worth knowing before you rely on it:

- **It only works on Linux.** macOS and Windows mount everything they recognise
  by themselves; a drive needing more than that is a job for Disk Utility or
  Disk Management.
- **It only works on the computer the drive is plugged into.** No program can
  partition a disk inside a machine it is not running on, so a drive in your
  NAS or in another computer has to be set up over there.

You can get around the second one by preparing the drive on a computer you can
sit at and then moving it — the usual approach for a headless Pi. One catch
worth knowing before you try it: a formatted drive moved to another machine
still will not appear until that machine is told to mount it, because the
mount instruction lives in `/etc/fstab` on the machine rather than on the
drive. [Preparing a drive on one computer to use in
another](troubleshooting-drives.md#preparing-a-drive-on-one-computer-to-use-in-another)
covers it.

[My drive doesn't show up](troubleshooting-drives.md) covers every case,
including the drive that is plugged into a different computer.

## Sharing one drive between two computers

You can. Backups are filed under the name of the computer that made them —
`<machine>/<folder>/` — so a `Documents` folder from the desktop and a
`Documents` folder from the laptop are separate directories and never overwrite
each other. Each computer also keeps its own manifest and its own status page in
its own folder, and the page at the top of the drive lists them all.

**The one thing that would go wrong is caught for you.** The machine name
defaults to the computer's hostname, and two fresh installs of the same system
really can both be called `ubuntu`. Two computers sharing a name would file
their backups in one directory, and each would then treat the other's files as
files that no longer exist and version them away. So the first computer to use a
name on a destination claims it, and a second one is refused — at setup, with
the remedy on screen, rather than silently later.

If that happens, give the second computer **a folder of its own on the drive**.
In the wizard that is *"Or put backups in a folder on this drive"* beside the
drive you picked; from the terminal:

```sh
backup-maker add-target drive /media/you/CARD/laptop --create
```

The rest of the drive is left alone, so this also works for a drive you use for
other things. Only take a name over (`--take-over`) when the computer that
claimed it really is this one — reinstalled, or restored from a backup.

## See also

- [hardware.md](../setup/hardware.md) — what to buy, in
  categories rather than brands, and how to avoid counterfeit media.
- [Setting up a Raspberry Pi as a backup target](../setup/raspberry-pi.md) — the
  always-on box, built step by step.
- [Sleeping computers](../setup/sleeping-computers.md) — why "always-on" is the word
  that matters in the table above.
- [When a destination fills up](../reference/space.md) — what happens when the box you chose
  runs out of room.
- [My drive doesn't show up](troubleshooting-drives.md) — blank, unmounted,
  read-only, or plugged into a computer this one cannot reach.
