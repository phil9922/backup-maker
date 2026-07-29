# My drive doesn't show up

You plugged a drive in, opened the dashboard, and it isn't in the list. This
page covers every reason that happens, in the order they are worth checking.

> **Setting up a drive you have just bought?** That is not a fault — a new
> drive has no filesystem and cannot be used until it has one. [Preparing a new
> drive](preparing-a-drive.md) is the page you want.

**Start here:** open the wizard and look at the list under the computer the
drive is plugged into. A drive that is attached but not usable is shown greyed
out, with the reason written next to it. If you can see it there, jump to the
matching section below. If you cannot see it at all, start at
[The computer can't see it either](#the-computer-cant-see-it-either).

---

## "Plugged in, but not set up for backups yet: there are no partitions on it"

The drive is brand new. It has no partitions and no filesystem, so there is
nothing for the computer to mount and nowhere to put files. This is the normal
state of a drive out of its box.

**Fix:** open *Set this drive up…* beside it. That erases the drive, makes one
ext4 filesystem on it, and mounts it — permanently, so it comes back after a
reboot.

If the button is not offered, the dashboard shows the command to run instead,
along with a one-off line that grants it permission to do this for you in
future. Both are printed on the page with the exact device and size filled in.
[Preparing a new drive](preparing-a-drive.md) walks through the whole thing.

> **This erases the drive.** Only do it to a drive whose contents you do not
> want. backup-maker refuses to touch a drive that has *anything* on it, so if
> the drive has been used before you will have to erase it yourself first —
> deliberately, with your own hands, which is the point.

## "Plugged in and already partitioned, but nothing on it is mounted"

The drive has a filesystem, but this computer has not mounted it, so it has no
path and cannot receive files. Usual causes: a headless machine with no desktop
session to mount it automatically, or a filesystem this machine can't read.

**Fix, one time only:**

```sh
lsblk -f                      # find the partition and its type
sudo mkdir -p /mnt/backups
sudo mount /dev/sda1 /mnt/backups
```

**Fix, permanently** — add it to `/etc/fstab` so it comes back after a reboot:

```sh
blkid -s UUID -o value /dev/sda1
echo 'UUID=<uuid>  /mnt/backups  ext4  defaults,noatime,nofail  0  2' \
    | sudo tee -a /etc/fstab
sudo mount /mnt/backups
```

Use the drive's UUID, not `/dev/sda1`: device names change when you add or
remove disks, and a destination that moves is one backup-maker will refuse to
write to. `nofail` means a missing drive does not stop the machine booting.

If the filesystem is exFAT or NTFS, the driver may be missing:

```sh
sudo apt install exfatprogs      # exFAT
sudo apt install ntfs-3g         # NTFS
```

ext4 is the better choice for a drive only this machine will ever touch.

## "Nothing is mounted here"

You are looking at a folder like `/mnt/backups` that has no drive mounted on
it. It is an ordinary directory on the computer's own disk, and it is shown as
a warning rather than offered as a destination on purpose.

This is the most expensive mistake this program can help you avoid. Backups
sent there would fill the machine's own disk while looking exactly like they
were going to an external drive — and they would be lost with the machine they
were meant to survive.

**Fix:** mount the drive that belongs there (see the section above). If there
is genuinely no drive and you meant to back up to a folder on this computer,
use *Or choose any folder on this computer* — that path is still open, it just
isn't offered as if it were a drive.

To make this failure loud rather than silent, protect the empty directory so
nothing can be written to it while the drive is absent:

```sh
sudo chattr +i /mnt/backups     # nothing can be written here while unmounted
```

The flag applies to the directory underneath. Once a drive is mounted over it,
the drive's own permissions apply and writes work normally.

## "The drive says it is read-only"

The kernel has the whole disk marked read-only. Check for a physical
write-protect switch on the drive or its caddy — SD cards and some USB
enclosures have one. Failing that, the drive may be failing and have put
itself into a protective read-only state; check `dmesg` for I/O errors and
copy anything you care about off it now.

## The computer can't see it either

If the drive isn't in the dashboard *at all* — not even greyed out — the
machine has not detected the hardware.

```sh
lsblk                    # is it listed?
dmesg | tail -30         # what happened when you plugged it in?
```

- **Nothing at all in `dmesg`**: a cable or power problem. USB drives,
  especially spinning ones, often need more power than a small computer's port
  supplies. Try a different cable first, then a powered USB hub.
- **It appears and disappears repeatedly**: almost always power.
- **It's in `lsblk` but not on the dashboard**: it is probably in use — a disk
  with anything mounted or swapping on it is deliberately never offered, so
  the system disk can never be reformatted by accident.

## The drive is in another computer

A disk plugged into your NAS, your router, or another machine on the network
**cannot be set up from this computer's dashboard**. No program can partition
a disk inside a computer it is not running on, and backup-maker does not
pretend otherwise.

What this computer can see of a machine on the network is the folders that
machine already shares. If you have just plugged a drive into it, that drive
has to be set up over there first.

Two ways forward:

1. **Do it on that machine's terminal** using the commands in the sections
   above, then share the mounted folder (SMB/Samba). Nothing needs to be
   installed on it.
2. **Prepare the drive on this computer and move it across.** Plug it in here,
   set it up, then carry it over — and add one `/etc/fstab` line on the machine
   you moved it to, or it will still not appear. See [Preparing a drive on one
   computer to use in
   another](#preparing-a-drive-on-one-computer-to-use-in-another).
3. **Install backup-maker on that machine**, and use its own dashboard. Expand
   the machine in the wizard and open *Set up … as a backup computer* — the
   page generates the exact commands for it, pinned to the version you are
   running. It cannot tell what kind of computer it is, so pick the platform
   from the list.

   Note that on a machine with no screen you will also need an SSH tunnel to
   reach its dashboard (`ssh -L 8666:localhost:8666 you@thatmachine`) — the
   dashboard listens on loopback only, and the read-only network view
   deliberately cannot change anything. If you are already in a terminal on
   that machine, running the `prepare-drive` command there is fewer steps than
   the tunnel.

A useful check: look at the size of a share you already have. If it reports
29GB when you expected 500GB, the shared folder is on that machine's own system
disk, not on the drive you meant — the drive is probably not mounted over it.

## Preparing a drive on one computer to use in another

The usual way to ready a drive for a Pi or a NAS: prepare it on a computer you
can sit at, then carry it over. It works, with one catch that catches almost
everybody:

> A filesystem travels with the drive. **The instruction to mount it does
> not** — that lives in `/etc/fstab` on each machine, so it has to be added
> again on the machine you move the drive to.

So a drive formatted on your laptop and plugged into a Pi is still invisible
until the Pi is told to mount it. The drive is not the problem, and
reformatting will not help.

The full sequence — including the UUID to carry across and the fstab line to
remove from the machine you borrowed — is in [Preparing a new
drive](preparing-a-drive.md#preparing-it-elsewhere-and-moving-it).

## Why did prepare-drive refuse?

It says which of these it is, and every one of them is deliberate:

- the device is not a whole disk (a partition, or a `/dev/disk/by-id` alias)
- anything is mounted from it, or it is in use as swap — this is what keeps the
  system disk permanently out of reach
- it has **any** filesystem or partition table on it already. A used drive must
  be cleared by you first, deliberately, so the decision is yours and stays
  reversible up to the moment you make it
- a folder this computer backs up lives on it
- the mount point is inside a folder this computer backs up, or is not empty
- the confirmation phrase does not match the drive — check you are looking at
  the row you think you are

To let the dashboard run it for you, once per machine. The program has to be
installed somewhere only root can write first — granting passwordless root to a
file you can overwrite would hand over root itself, so it refuses:

```sh
sudo install -o root -g root -m 755 ~/.local/bin/backup-maker /usr/local/bin/backup-maker
sudo /usr/local/bin/backup-maker prepare-drive --install-sudoers
```

It prints the permission it is asking for and waits for you to type `yes`. What
that permission does and does not allow is in
[Security](../reference/security.md#preparing-a-drive). Remove it at any time
with `sudo rm /etc/sudoers.d/backup-maker`; nothing else stops working.

## On macOS and Windows

backup-maker does not format drives on either. Both mount any filesystem they
recognise by themselves, so a drive that needs setting up needs a tool that
explains itself better than this one could:

- **macOS** — Disk Utility. Erase the drive, and it appears under `/Volumes`.
- **Windows** — Disk Management (`diskmgmt.msc`). Initialise and format the
  disk, and once it has a drive letter the dashboard will offer it.

---

Still stuck? [Open an issue](https://github.com/phil9922/backup-maker/issues)
with the output of `lsblk -f` and `backup-maker status`.
