# Preparing a new drive

You have bought a drive to keep backups on. This page takes it from the
packaging to a working destination.

**A drive out of the box has nothing on it** — no partition table, no
filesystem. Plugging it in is not enough for any operating system to use it,
which is why a new drive often appears to do nothing at all. This is normal and
it is a one-off: once prepared, a drive stays prepared.

Three questions decide what you do:

| | Go to |
|---|---|
| Drive plugged into the computer you are sitting at, running Linux | [The quick way](#the-quick-way-dashboard) |
| Drive destined for a Pi, NAS or other machine with no screen | [Preparing it elsewhere and moving it](#preparing-it-elsewhere-and-moving-it) |
| You are on macOS or Windows | [macOS and Windows](#macos-and-windows) |

---

## Before you start: which format?

**ext4**, unless you have a specific reason not to. It handles file permissions
and large files properly, and every Linux machine reads it without installing
anything.

Choose otherwise only if the drive will be plugged directly into more than one
kind of computer:

| Format | Use it when | Cost |
|---|---|---|
| **ext4** | Only Linux machines will touch the drive. **The default, and what backup-maker sets up.** | Windows and macOS cannot read it without extra software. |
| **exFAT** | You need to plug the same drive into Windows or macOS directly. | No file permissions or symlinks. A Pi needs `sudo apt install exfatprogs`. |
| **NTFS** | You are reusing a drive already formatted by Windows and would rather not erase it. | Needs `ntfs-3g` on Linux; slower, and the worst of the three for this job. |

Backing up *from* Windows or macOS **to** a Linux machine over the network does
not require any of this — that traffic goes over SMB and the drive's own format
never comes into it. This choice only matters for a drive you physically move
between machines.

## The quick way (dashboard)

With the drive plugged into a Linux computer running backup-maker:

1. Open the dashboard and start setting up a backup.
2. On **Where should the copies go?**, expand the computer the drive is
   plugged into. The drive is listed, greyed out:

   > ⚠ **Ugreen Storage Device** `/dev/sda` 465.8GB, USB
   > Plugged in, but not set up for backups yet: there are no partitions on it.
   > ▸ Set this drive up…

3. Open **Set this drive up…**. Check the mount point, then type the drive's
   own name and size back to confirm and press **Erase and set up this drive**.

The first time on any machine, it will tell you it has not been given
permission and show you what to run once. The program has to be installed
somewhere only root can write before it will grant this — a file you can
overwrite yourself is one anything running as you can overwrite, and passwordless
root on it would be root itself rather than the narrow permission being asked for:

```sh
sudo install -o root -g root -m 755 ~/.local/bin/backup-maker /usr/local/bin/backup-maker
sudo /usr/local/bin/backup-maker prepare-drive --install-sudoers
```

That prints the exact permission being asked for and waits for you to type
`yes`. Reload the dashboard afterwards and the button works. What that
permission does and does not allow is in
[Security](../reference/security.md#preparing-a-drive); you never have to grant
it, and the page shows you the command to run yourself instead.

> **This erases the drive.** backup-maker refuses any drive that has *anything*
> on it, so a used drive has to be cleared by you first — deliberately. That is
> the intended friction.

## The same thing from a terminal

Identical result, and the only route on a machine with no screen.

**1. Find the drive, and be certain of the name.**

```sh
lsblk -o NAME,SIZE,MODEL,TRAN,MOUNTPOINT
```

Look for the size and model you recognise, and `usb` in the `TRAN` column. Note
that the disk carrying `/` is the machine's own — erasing it costs you the
operating system. backup-maker will refuse to touch it, but read the list
anyway.

**2. See exactly what would happen.**

```sh
sudo backup-maker prepare-drive \
    --device /dev/sda --mount /mnt/backups --label BACKUPS \
    --confirm "sda 465.8GB" --dry-run
```

`--dry-run` prints every command it would run and changes nothing. The
`--confirm` phrase is the device name and its size — the dashboard shows it,
and `prepare-drive` tells you the expected phrase if you get it wrong.

**3. Do it**, by running the same command without `--dry-run`.

It refuses, saying which, when: the device is not a whole disk; anything is
mounted from it or it is in use as swap; it already has any filesystem or
partition table; a folder this computer backs up lives on it; the mount point
overlaps a folder being backed up, or is not empty; or the phrase does not
match.

### Doing it with standard tools instead

`prepare-drive` is a convenience, not a requirement. This is all it does:

```sh
sudo sgdisk --clear --new=1:0:0 --typecode=1:8300 /dev/sda
sudo mkfs.ext4 -m 1 -L BACKUPS /dev/sda1
blkid -s UUID -o value /dev/sda1                   # note this UUID
sudo mkdir -p /mnt/backups
sudo chattr +i /mnt/backups                        # see below
echo 'UUID=<uuid>  /mnt/backups  ext4  defaults,noatime,nofail  0  2' \
    | sudo tee -a /etc/fstab
sudo mount /mnt/backups
sudo chown $USER:$USER /mnt/backups
```

## Preparing it elsewhere and moving it

The usual way to get a drive ready for a Raspberry Pi or a NAS: prepare it on a
computer you can sit at, then carry it across.

It works — but formatting is only half the job:

> A filesystem travels with the drive. **The instruction to mount it does
> not** — that lives in `/etc/fstab` on each machine, so it has to be added
> again on the machine you move the drive to.

A desktop hides this by mounting drives for you the moment they are plugged in.
A machine with no screen has no desktop session doing that, so a perfectly good
drive plugged into a Pi sits there: present, formatted, mounted nowhere, and
therefore invisible. The drive is not the problem and reformatting will not
help.

**1. Prepare it** on the computer with a screen, as above.

**2. Write down the UUID** — this is what the other machine uses to find it:

```sh
lsblk -f                          # or:
blkid -s UUID -o value /dev/sda1
```

**3. Tidy up the machine you used.** Preparing the drive added an `/etc/fstab`
entry *here*, pointing at a drive that is about to leave. `nofail` means it
does no harm, but it is misleading later:

```sh
sudo umount /mnt/backups
sudo sed -i '/\/mnt\/backups/d' /etc/fstab       # check the file afterwards
```

**4. Move the drive**, and on the machine you moved it to:

```sh
sudo mkdir -p /mnt/backups
sudo chattr +i /mnt/backups                      # BEFORE mounting — see below
echo 'UUID=<the uuid>  /mnt/backups  ext4  defaults,noatime,nofail  0  2' \
    | sudo tee -a /etc/fstab
sudo mount /mnt/backups
sudo chown $USER:$USER /mnt/backups
```

**5. Check you are looking at the drive**, not the empty folder underneath it:

```sh
df -h /mnt/backups     # must name the drive, not the system disk
```

## Two details that matter later

### Mount by UUID, never by `/dev/sda`

Device names are not stable. `/dev/sda` is whichever disk the kernel saw first;
add another drive and today's `sda` can be tomorrow's `sdb`. backup-maker
refuses to write to a destination whose identity marker doesn't match what it
expects — correct behaviour, and thoroughly baffling when the cause is a drive
that quietly moved. A UUID belongs to the filesystem and travels with it.

### `nofail`, and the immutable empty directory

**`nofail`** in the fstab line means a missing drive does not stop the machine
booting. Without it, a Pi whose drive failed to appear stops at boot — and a Pi
with no screen or keyboard is then simply gone.

**`chattr +i` on the empty mount point** turns a silent failure into a loud
one. `/mnt/backups` exists whether or not the drive is mounted over it. If the
drive ever fails to appear, an unprotected directory quietly accepts backups
onto the machine's own system disk while looking exactly as though they are
going to the drive — filling the boot disk, and losing the backups along with
the machine they were meant to outlive. Making the bare directory immutable
means those writes fail instead.

The flag applies only to the directory underneath. Once the drive is mounted
over it, the drive's own permissions apply and writes work normally.
`prepare-drive` sets this for you; if you move the drive to another machine,
set it there too.

## macOS and Windows

backup-maker does not format drives on either — both mount any filesystem they
recognise by themselves, so a drive needing more than that is a job for a tool
that explains itself better than this one could.

- **macOS** — Disk Utility. Erase the drive, and it appears under `/Volumes`,
  where backup-maker will offer it.
- **Windows** — Disk Management (`diskmgmt.msc`). Initialise and format the
  disk; once it has a drive letter, backup-maker will offer it.

## See also

- [My drive doesn't show up](troubleshooting-drives.md) — when a drive is
  plugged in and still isn't offered.
- [Choosing destinations](3-destinations.md) — what to put where, and why more
  copies in more places beats one perfect one.
- [Raspberry Pi as a backup target](../setup/raspberry-pi.md) — the whole
  always-on box, drive included.
- [Security](../reference/security.md#preparing-a-drive) — exactly what the
  optional permission grants, and what stops it being abused.
