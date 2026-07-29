# Setting up a Raspberry Pi as a backup target

A Raspberry Pi with an external SSD is the cheapest always-on backup
destination: a few watts, silent, and with no sleep mode to fall into. This
guide takes you from an empty microSD card to a Pi that receives your backups
around the clock, with nothing but another computer on the same network.

Every step here was performed for real on a Raspberry Pi 5 while writing this
guide — including the failures. Where something bit us, the guide says so.

**What you end up with:** a headless Pi (no monitor, no keyboard) in a corner,
wired to your router, sharing an external SSD that backup-maker mirrors to in
real time and snapshots on a schedule. You never touch the Pi again except to
glance at its status page.

## Why an always-on destination matters

This is the single most important choice you'll make, so it's worth being
explicit about why:

1. **Real-time backups only happen while the destination is awake.** A drive
   attached to a computer that sleeps, or a machine you switch off at night,
   receives nothing while it's down. Your backup is only as current as the last
   moment that destination was reachable — and you won't notice the gap until
   you need the file.
2. **It survives the fate of your computer.** A card left in your laptop's slot
   is genuinely useful and costs nothing extra, but theft, a power surge, a
   spilled drink or ransomware take the laptop and that card together. A
   separate box on the far side of the room doesn't share those.
3. **You can check on it independently.** If your computer is off, broken or
   stolen, an always-on destination is still sitting there on your network: you
   can browse it from a phone or another PC and confirm your files are really
   present, with the dates you expect. A drive that only exists inside the dead
   machine leaves you nothing to look at until it comes back.

A Raspberry Pi with an external SSD is the cheapest way to get all three, and
building one is what the rest of this page is about.

## What the Pi is for

**Its job is to keep storage reachable when your computer is off.** It is an
always-on box holding one or more external drives, sitting on your network so
backups can land there at 3am with the laptop shut. Because it stays powered, it
is also the machine that can serve you a
[status page](../guide/5-monitoring.md#checking-backups-when-your-computer-is-off)
you can open from your phone to check that backups are still working.

**Nothing on the Pi is backed up.** It is a destination, not something being
protected — there is no need for anything on it to be worth keeping. If the Pi
itself dies you replace it, reattach the drives, and carry on.

That is the whole job, and it is what the rest of this page builds.

## How backup-maker reaches it

A secondary choice, and most people can take the first option without thinking
about it.

**Over Samba, as a network drive — what this guide builds.** Install Samba on
the Pi, share the drive, add it with `backup-maker add-target share`. Nothing
runs on the Pi but Samba, and backup-maker never has to be installed or
maintained there.

**As a paired machine — the upgrade.** Run backup-maker on the Pi too and pair
the two. `receive enable` makes the Pi a *destination*; it still backs nothing up
of its own. What changes is only how the bytes travel: block-level delta sync
with verification, and mutual TLS instead of a Samba password.

Whether that is worth a second install depends entirely on what you protect.
Change 10MB inside a 4GB file and Samba rewrites the whole 4GB, because SMB
cannot know which parts moved; delta sync sends roughly the 10MB that did. So it
earns its keep for **large files that change slightly** — VM images, databases,
disk images. For **many small files** it buys almost nothing, since a changed
file is sent whole either way, and you would be maintaining a second install, a
second autostart unit and a second thing to upgrade. As the README puts it, this
is an upgrade, not the point — you never need it.

**Steps 1 to 7 below build the Samba route.** The paired-machine route shares
steps 1 to 4 exactly, and then
[the paired-machine route](#alternative-run-backup-maker-on-the-pi) replaces
steps 5 and 7.

## Pi or NAS?

Both work, and backup-maker treats them the same way — a NAS is just a network
drive, and so is a Pi running Samba.

| | Raspberry Pi + external SSD | NAS |
| --- | --- | --- |
| Cost | a fraction of a NAS | several times more |
| Power / noise | a few watts, silent | more, often audible |
| Capacity | one drive in this guide; more on the spare USB ports, with the power caveat below | several, expandable |
| Disk redundancy | none | RAID on multi-bay models |
| Setup | you install the OS and Samba | works out of the box |

**Don't over-value the redundancy column.** A NAS's RAID protects against *a
disk failing*. It does not protect against deleting a file, a bad edit,
ransomware, theft, or fire — the mirror faithfully copies all of those to both
disks instantly. RAID is uptime insurance, not a backup. That makes a
single-disk Pi a much smaller compromise than it first appears, as long as you
keep **more than one destination** — which is the whole reason backup-maker
lets you tick several at once.

Buy a NAS if you need the capacity or want the convenience. For protecting one
or two computers, a Pi with a good external SSD does the same job for the
money you'd spend on the NAS's empty enclosure.

## What you need

- **A Raspberry Pi** — this guide uses a Pi 5, but any 64-bit-capable model
  (Pi 3 onward) works. Check [Building for the Pi](#building-for-the-pi) below
  for the OS-architecture details.
- **The official power supply, or one genuinely rated 5V/5A** (Pi 5). This is
  not pedantry: a weaker supply makes the Pi clamp its USB ports to 600mA, and
  a bus-powered SSD then browns out mid-write. That failure *looks like a
  dying disk* — you will debug the wrong thing. Third-party "compatible" bricks
  that sag under load cause the same thing.

  **Running more than one external drive? Use the 45W supply.** Every drive you
  hang off the USB ports draws from the same budget, and the 27W supply does not
  leave much headroom once a second disk spins up — you get the same brown-out,
  intermittently, under load, which is the hardest version of this fault to
  diagnose. The alternative is drives with their own mains power, which take
  nothing from the Pi at all; a powered USB hub does the same job for
  bus-powered disks.
- **A microSD card for booting** — 16GB is plenty. The OS lives here and
  *nothing else does*; backups never touch this card, because microSD cards
  wear out under constant writing.
- **An external SSD in a USB enclosure** — this holds the backups. Size it at
  roughly 2× the data you're protecting; versioning keeps ~30 days of file
  history. See [hardware.md](hardware.md) for how to
  choose one (and how to avoid counterfeits).
- **An Ethernet cable.** Wired, not wifi — a destination that drops off wifi
  reads as offline and silently stops receiving backups. The whole point of
  this box is that it's boringly always there.
- No monitor, no keyboard. Everything happens over the network.

**Two gotchas before you plug anything in (Pi 5):**

- The Pi 5's **USB-C port is power only**. The SSD goes into one of the
  **blue USB-A** ports (USB 3). Many SSD enclosures ship only a C-to-C
  cable, so you may need a C-to-A cable or adapter.
- If your enclosure takes M.2 drives, check the drive's form factor fits
  (2230 vs 2280) *before* assembly day.

## Pi-specific storage advice

Worth knowing before you assemble anything — these decide whether the drive
behaves for years or misbehaves in ways that look like a failing disk:

- **Use a powered USB hub or a self-powered drive** for spinning USB disks, or
  if the drive misbehaves even on a good supply. An under-powered Pi drops the
  drive mid-write, which shows up as a destination that keeps going offline.
- **Mount by UUID in `/etc/fstab`** so the drive lands at the same path after
  every reboot — backup-maker refuses to write to a target whose marker file
  doesn't match, which is exactly what a shuffled `/media/...` path looks
  like. Add `nofail` so a missing drive doesn't block boot.
- **ext4 is the right format** for a drive only the Pi will touch. Use exFAT
  only if you'll also plug it into Windows/macOS directly (`sudo apt install
  exfat-fuse`).

## Step 1 — Flash the OS, headless

Install [Raspberry Pi Imager](https://www.raspberrypi.com/software/) on any
computer with an SD card slot — and check the version before trusting it:

```sh
rpi-imager --version    # must be 2.x
```

**Imager 1.x silently breaks headless setup with current Raspberry Pi OS
images.** Recent images (Debian 13 "trixie" onward) apply your settings via
cloud-init, which 1.x doesn't understand — it accepts your hostname, user and
SSH settings, then writes a completely stock image without a word of
complaint. The result only boots to a login prompt you can reach with a
monitor and keyboard. We flashed twice with Debian's packaged 1.8.5 before
discovering this; the versions in Linux distro repositories are often still
1.x, so on Debian/Ubuntu skip `apt` and install the official package:

```sh
curl -LO https://downloads.raspberrypi.org/imager/imager_latest_amd64.deb
sudo apt install ./imager_latest_amd64.deb
```

Then, in Imager:

1. **Choose Device** → your Pi model (this only filters the OS list).
2. **Choose OS** → *Raspberry Pi OS (other)* → **Raspberry Pi OS Lite
   (64-bit)**. Lite because the Pi will never show a desktop; 64-bit because
   the sync engine ships for `aarch64` (32-bit ARM is not supported — see
   [Building for the Pi](#building-for-the-pi)).
3. **Choose Storage** → the microSD card. If the card came preloaded with an
   OS, that's fine — it gets overwritten.
4. In the customisation screens that follow, whatever your version calls
   them:
   - **Hostname**: something you'll recognise on the network, e.g.
     `backup-pi`.
   - **Username and password**: set your own. Write the password down — this
     is a box you'll touch so rarely you *will* forget it.
   - **Enable SSH** with password authentication (you can tighten this
     later). It's sometimes on its own tab or page — don't miss it.
   - **Skip wifi** entirely; the Pi will be wired.
   - **Skip Raspberry Pi Connect** if offered. It's a cloud remote-access
     service tied to an online account; this box's whole job is to be
     local-only, and SSH on your own network is all the access it needs.
5. Write and wait for the verify pass to finish.

**Before the card goes anywhere near the Pi**, check the settings actually
landed — a stock image looks identical from the outside. Re-mount the card
and look at `user-data` on its small `bootfs` partition:

```sh
grep "^hostname" /media/$USER/bootfs/user-data
```

That should print the hostname you chose. If it prints nothing (every line
in the file commented out), your settings were discarded — you're on Imager
1.x territory; install 2.x and flash again. Thirty seconds of looking beats
a monitor hunt after a silent failed boot.

## Step 2 — First boot

Connect Ethernet, then power, and give it a couple of minutes — the first
boot resizes the filesystem and reboots itself once.

Then, from your computer:

```sh
ssh yourname@backup-pi.local
```

(replace `yourname` and `backup-pi` with the username and hostname you set in
Imager). If `.local` doesn't resolve on your network, find the Pi's address
in your router's device list and `ssh yourname@<that address>` instead.

Two prompts appear on the first connection, and both are about **the Pi, not
the computer you're typing on**:

- *"Are you sure you want to continue connecting?"* — the Pi is a machine
  your computer has never met; type `yes`.
- `yourname@backup-pi.local's password:` — this wants **the password you set
  in Imager for the Pi**, not your computer's login password. The
  `@backup-pi.local` in the prompt is how you can tell who's asking.

Typing the password every time gets old, so teach the Pi to recognise your
computer instead — run these on your computer, not the Pi:

```sh
ssh-keygen -t ed25519          # skip if ~/.ssh/id_ed25519 already exists
ssh-copy-id yourname@backup-pi.local
```

`ssh-copy-id` asks for the Pi's password one final time; after that, SSH
logs straight in.

With the key working, turn off password logins. This box will sit on your
network for years; the fewer ways in, the better — and if your ISP gives out
IPv6, SSH may be reachable from further away than you think:

```sh
sudo tee /etc/ssh/sshd_config.d/10-hardening.conf >/dev/null <<'EOF'
PasswordAuthentication no
KbdInteractiveAuthentication no
PermitRootLogin no
EOF
sudo sshd -t && sudo systemctl reload ssh
```

The filename matters. Raspberry Pi OS ships a `50-cloud-init.conf` that
enables password authentication, and sshd honours the **first** value it
reads, so a file sorting before it wins. Check it took effect, and confirm
your key still works **before closing this session**:

```sh
sudo sshd -T | grep ^passwordauthentication      # must say: no
```

If you ever lose the key, the micro-HDMI cable and a keyboard are the way
back in — that is what they're for.

Finally, install the two packages the coming steps need — Samba to share the
drive, and f3 to prove the drive is genuine before trusting it:

```sh
sudo apt install -y samba f3
```

(both run on the Pi). Then reboot once — `sudo reboot` — partly to land on
any kernel the upgrade brought, but mostly as a rehearsal: this box must
come back up with no monitor and nobody logged in, and it's better to learn
that it does while nothing depends on it. It should answer SSH again within
a minute.

Once you're in, two checks:

```sh
uname -m            # must print: aarch64
sudo apt update && sudo apt full-upgrade -y
```

If `uname -m` prints `armv7l` or `armv6l`, the card was flashed with a 32-bit
image — go back to step 1 and pick the 64-bit Lite image, because there is no
32-bit ARM build of the sync engine.

## Step 3 — Prove the storage is genuine before you trust it

Counterfeit cards and sticks are common: they report a large capacity, accept
everything you write, and silently discard whatever passes their real size.
You find out when you try to restore. Test *every* piece of storage that will
hold backups — the card in your laptop as much as the drive on the Pi.

```sh
sudo apt install -y f3
```

**The fast check — is the capacity real?** This writes directly to the device,
so it destroys anything on it. Do it before you put a filesystem on:

```sh
sudo f3probe --destructive --time-ops /dev/mmcblk0     # your device, not this one
```

On a genuine 128GB card this took **38 seconds** and said:

```
Good news: The device `/dev/mmcblk0' is the real thing
             *Usable* size: 119.38 GB (250347520 blocks)
            Announced size: 119.38 GB (250347520 blocks)
```

Usable and announced must match. If usable is dramatically smaller, the card
is a fake — return it, and do not put anything on it you would miss.

**The thorough check — is every block good?** `f3probe` catches fraud, not
worn or defective sectors. Once the device has a filesystem and is mounted,
fill it completely and read it all back:

```sh
f3write /media/you/BACKUPCARD      # fills the free space with test files
f3read  /media/you/BACKUPCARD      # reads every one back and verifies
rm /media/you/BACKUPCARD/*.h2w     # delete the test files afterwards
```

Budget roughly an hour for 128GB — it runs at whatever your reader can
sustain. A genuine, healthy card finishes like this:

```
Free space: 0.00 Byte
Average writing speed: 48.90 MB/s
...
  Data OK: 119.35 GB (250303744 sectors)
Data LOST: 0.00 Byte (0 sectors)
	       Corrupted: 0.00 Byte (0 sectors)
	Slightly changed: 0.00 Byte (0 sectors)
	     Overwritten: 0.00 Byte (0 sectors)
Average reading speed: 83.72 MB/s
```

Every one of those loss figures must be zero. Anything else means the card is
losing data *today*, before you have trusted it with anything. Neither command
needs root once the card is mounted and writable by you — only `f3probe` does.

## Step 4 — Format and mount the SSD

A brand-new drive arrives with **nothing on it at all** — no partitions, no
filesystem. Plugging it in is not enough: Linux has nothing to mount, and on a
headless Pi there is no desktop session to mount it automatically even once
there is. Until this step is done the drive is invisible to everything,
including backup-maker, which is exactly as it should be.

You can do this from the dashboard or from the terminal. **Both do the same
thing**, and the dashboard shows you the command it is about to run before it
runs it.

> **Thinking of preparing the drive on your laptop first and then plugging it
> into the Pi?** That works, and for a headless Pi it is often the easier
> route — but formatting it elsewhere is only half the job. The filesystem
> travels with the drive; the instruction to *mount* it does not, because that
> lives in `/etc/fstab` on each machine. A drive formatted on a laptop and
> plugged into a Pi will sit there mounted nowhere, and so stay invisible.
> [Preparing a drive on one computer to use in
> another](../guide/troubleshooting-drives.md#preparing-a-drive-on-one-computer-to-use-in-another)
> has the full sequence, including the UUID you need to carry across.

### From the dashboard

This needs backup-maker running on the Pi (Step 6). If you are setting the Pi
up as a plain network drive and stopping at Step 5, use the terminal version
below instead.

Open the Pi's dashboard, start a backup, and reach **Where should the copies
go?**. The drive appears under this computer, greyed out:

> ⚠ **Ugreen Storage Device** `/dev/sda` 465.8GB, USB
> Plugged in, but not set up for backups yet: there are no partitions on it.
> ▸ Set this drive up…

Open *Set this drive up…*, check the mount point is `/mnt/backups`, type the
drive's size back to confirm, and press **Erase and set up this drive**.

The first time, it will tell you it has not been allowed to do this and give
you a line to run once. Because that permission lets the dashboard run this
program as root, the program has to live somewhere only root can write — so
install a copy there first, and grant the permission to that copy:

```sh
sudo install -o root -g root -m 755 ~/.local/bin/backup-maker /usr/local/bin/backup-maker
sudo /usr/local/bin/backup-maker prepare-drive --install-sudoers
```

If you skip the first line, the second refuses and tells you why: a file you
can overwrite yourself is a file anything running as you can overwrite, and
granting it passwordless root would hand over root itself rather than the
narrow permission being asked for.

It prints the exact permission it is asking for and waits for you to type
`yes`. Read it before you agree — it is three lines. Reload the dashboard
afterwards and the button works.

### From the terminal

```sh
lsblk -o NAME,SIZE,MODEL,TRAN,MOUNTPOINT
```

Find your drive and **be certain of the name**. `sda` on this Pi is the SSD;
`mmcblk0` is the card the Pi boots from. Erasing the wrong one costs you the
operating system.

```sh
sudo ~/.local/bin/backup-maker prepare-drive \
    --device /dev/sda --mount /mnt/backups --label BACKUPS \
    --confirm "sda 465.8GB" --dry-run
```

`--dry-run` prints every command it would run and changes nothing. Run it
without `--dry-run` when you are happy. It refuses outright if the drive has
anything on it, if anything is mounted from it, or if it holds a folder this
machine backs up — so a mistyped device name fails safely rather than
expensively.

If you would rather run the standard tools yourself, this is all it does:

```sh
sudo sgdisk --clear --new=1:0:0 --typecode=1:8300 /dev/sda
sudo mkfs.ext4 -m 1 -L BACKUPS /dev/sda1
blkid -s UUID -o value /dev/sda1                    # note the UUID
echo 'UUID=<uuid>  /mnt/backups  ext4  defaults,noatime,nofail  0  2' \
    | sudo tee -a /etc/fstab
sudo mount /mnt/backups
sudo chown $USER:$USER /mnt/backups
```

### Why by UUID, and why `nofail`

**By UUID** because device names are not stable. `/dev/sda` is whichever disk
the kernel happened to see first; add a second drive and today's `sda` can be
tomorrow's `sdb`. backup-maker refuses to write to a target whose marker file
doesn't match what it expects — which is the right behaviour, and completely
baffling when the cause is a drive that moved.

**`nofail`** because without it a drive that fails to appear stops the boot,
and a Pi with no screen or keyboard attached is then simply gone. With
`nofail` it boots, the destination is missing, and backup-maker tells you so.

### Check it worked

```sh
df -h /mnt/backups     # must show /dev/sda1, not /dev/mmcblk0p2
touch /mnt/backups/probe && rm /mnt/backups/probe
```

If `df` names the card rather than the SSD, the drive is **not** mounted and
you are looking at the bare directory underneath. Step 5 makes that failure
loud rather than silent; do not skip it.

## Step 5 — Share the drive on the network

This is what lets backup-maker treat the Pi as a network drive, with nothing
installed on it beyond Samba.

That is still true, and it is the simpler arrangement. The one thing it costs
you: a drive plugged into the Pi can only be *set up* on the Pi, because no
program can format a disk inside a computer it is not running on. Sharing a
drive over the network does not change that. So with Samba alone, Step 4 is a
terminal job — which is why Step 4 gives the commands as well as the buttons.
Step 6 puts backup-maker on the Pi if you would rather do it from a browser.

**First, protect the mount point from its own absence.** If the SSD ever
fails to mount — a dead enclosure, a changed UUID, a knocked cable — a share
pointing at `/mnt/backups` would cheerfully accept backups into the empty
directory *underneath* the mount, quietly filling the boot card with the
backups you thought were on the SSD, and wearing out the card holding your
OS. Make the bare directory immutable, so that failure is loud instead:

```sh
sudo mkdir -p /mnt/backups
sudo chown $USER:$USER /mnt/backups
sudo chattr +i /mnt/backups        # nothing can be written here while unmounted
```

The flag applies to the *underlying* directory only. Once the SSD is mounted
over it, the drive's own permissions apply and writes work normally.

Now replace Samba's stock configuration. Debian ships one that offers every
user's home directory and the printers; a backup box should offer exactly one
thing:

```sh
sudo cp /etc/samba/smb.conf /etc/samba/smb.conf.orig
sudo nano /etc/samba/smb.conf
```

```ini
[global]
   workgroup = WORKGROUP
   server role = standalone server
   server string = backup destination

   # Only answer on the home network and loopback. Give the SUBNET, not the
   # interface name: naming `eth0` binds every address that interface has,
   # and if your ISP hands out IPv6 that includes a globally routable one —
   # your backups' file server, offered to the internet, protected only by
   # whatever your router happens to block. We found ours bound to a public
   # 2605:… address exactly this way.
   interfaces = lo 192.168.1.0/24
   bind interfaces only = yes

   # backup-maker speaks SMB2/3; SMB1 is obsolete and a liability.
   server min protocol = SMB2_10
   client min protocol = SMB2_10

   # No anonymous access, and a wrong username is refused rather than
   # silently downgraded to guest.
   map to guest = never
   usershare allow guests = no
   restrict anonymous = 2

   # No printing on a backup box.
   load printers = no
   printing = bsd
   printcap name = /dev/null
   disable spoolss = yes

   logging = file
   log file = /var/log/samba/log.%m
   max log size = 1000

[backups]
   comment = backup-maker destination
   path = /mnt/backups
   browseable = yes
   read only = no
   guest ok = no
   valid users = YOURUSER
   force user = YOURUSER
   force group = YOURUSER
   create mask = 0644
   directory mask = 0755
```

Replace `YOURUSER` with your username and `192.168.1.0/24` with your own
network — `ip -o -4 addr show` prints the Pi's address and prefix, so
`192.168.5.141/22` means `192.168.4.0/22`. Then check the file parses, give
yourself an SMB password — which is **separate from your login password** —
and restart:

```sh
testparm -s                         # should list only [global] and [backups]
sudo smbpasswd -a $USER
sudo smbpasswd -e $USER
sudo systemctl restart smbd
```

Confirm it is listening where you expect and nowhere else:

```sh
ss -tln | grep -E ':445|:139'       # loopback and your LAN address only
```

Verify from another computer, not from the Pi — that is the thing you
actually care about:

```sh
smbclient -L //<pi-address> -N                     # anonymous: must be refused
smbclient -L //<pi-address> -U <user>              # should list only "backups"
```

Anonymous access will connect to the server but must fail with
`NT_STATUS_ACCESS_DENIED` on the share itself. And while the SSD is not yet
mounted, a write must fail the same way — that's the immutable flag doing
its job, and it is worth confirming now rather than discovering later:

```sh
smbclient //<pi-address>/backups -U <user> -c 'put /etc/hostname test.txt'
# NT_STATUS_ACCESS_DENIED  ← correct while unmounted
```

## Step 6 — Serve the status page over HTTP (optional)

backup-maker writes a small `backup-maker-status.html` to each destination, so
you can check your backups' health from a phone even while your computer is off.
Each computer writes its own inside its own folder; the one at the destination
root lists them all and links to each, which is the one to serve. Serving it
needs care: **never point a web server at the backup root** — it would hand out
the backups themselves. Give it a directory containing nothing but a link to the
page:

```sh
sudo apt install -y nginx
sudo mkdir -p /var/www/backup-status
sudo ln -sf /mnt/backups/backup-maker-status.html /var/www/backup-status/index.html
```

Debian's nginx starts by serving a default welcome page on port 80 to your
whole network. Remove it and add a block that serves one path and nothing
else:

```sh
sudo rm -f /etc/nginx/sites-enabled/default
sudo tee /etc/nginx/conf.d/backup-status.conf >/dev/null <<'EOF'
server {
    listen 192.168.1.20:8668;
    server_name _;

    root /var/www/backup-status;
    autoindex off;

    location = / {
        try_files /index.html =404;
    }
    location / {
        return 404;
    }
}
EOF
sudo nginx -t && sudo systemctl restart nginx
```

Replace `192.168.1.20` with the Pi's address — binding to it rather than
`0.0.0.0` keeps the page off any other network the box is on — and use any
free port above 1024. A `reload` will not release port 80; `restart` does.

Check that it serves the page and refuses everything else:

```sh
curl -o /dev/null -w '%{http_code}\n' http://<pi>:8668/                    # 200
curl -o /dev/null -w '%{http_code}\n' http://<pi>:8668/my-laptop/code/x    # 404
```

Until the drive is mounted and a backup has run, `/` correctly returns 404 —
the page it links to doesn't exist yet.

**Know what you're publishing:** this is plain HTTP with no authentication,
so anyone on your network can read which folders you protect and whether
they're in sync. It never exposes file contents or paths inside them, and
the page says so itself when it's stale.

## Step 7 — Point backup-maker at it, and prove it works

> Not yet written — it needs the mounted drive from steps 3 and 4. It will
> cover adding the share from the setup wizard (or
> `backup-maker add-target share //<pi>/backups --user <user>`), the three
> backup jobs worth running, and deliberately filling a destination to watch
> space reclamation delete old versions while leaving the live mirror intact.

In the meantime, [Your first backup](../guide/2-first-backup.md#adding-backup-targets)
covers adding a network drive from the CLI, and
[Choosing destinations](../guide/3-destinations.md) describes the three backup jobs.

## Building for the Pi

This matters only if you'll run backup-maker on the Pi (the paired-machine
route), and it's why steps 1 and 2 insist on a 64-bit image. The Samba route
this guide builds installs nothing on the Pi beyond Samba, so it does not care.

Check which OS you're on first — it decides everything:

```sh
uname -m      # on the Pi
```

- **`aarch64`** — 64-bit Raspberry Pi OS. Fully supported. Cross-compile from
  your main machine:

  ```sh
  GOOS=linux GOARCH=arm64 go build -trimpath -o backup-maker-pi .
  scp backup-maker-pi pi@raspberrypi.local:~/backup-maker
  ```

- **`armv7l`** — 32-bit Raspberry Pi OS. Build with `GOARCH=arm GOARM=7`.
  Drive and network-drive targets work normally, but **paired-machine targets
  need Syncthing installed yourself** (`sudo apt install syncthing`) — there
  is no pinned 32-bit ARM build to download, and backup-maker will fall back
  to the system one. If you have the choice, reinstall with 64-bit Pi OS.

Pi 3 and newer can run 64-bit; Pi Zero (original) and Pi 1 are ARMv6 and are
not practical targets.

## Alternative: run backup-maker on the Pi

This is the paired-machine route — the upgrade described at the top of the page,
and not something you need. It replaces steps 5 and 7 above:
instead of sharing the drive over Samba, the Pi runs backup-maker itself and
the two machines pair, which buys you block-level delta sync with
verification.

On the Pi:

```sh
./backup-maker init
./backup-maker receive enable --root /mnt/backups
./backup-maker pair                     # prints device ID + ASCII QR code
./backup-maker autostart enable
sudo loginctl enable-linger $USER       # ESSENTIAL on a headless Pi
```

That last line is not optional. Autostart installs a **systemd user unit**,
and without lingering enabled, systemd stops your user's services the moment
your SSH session ends — the backup daemon would die every time you log out.

`backup-maker pair` prints the device ID and a QR code you can scan with your
phone instead of typing 63 characters over SSH. The Pi's own dashboard also
shows the ID as a QR code.

Then on your main machine, either add it in the dashboard (via the setup
wizard's "Add a computer by device ID" section) or from the CLI:

```sh
backup-maker add-target device <PI-DEVICE-ID>
```

Back on the Pi, approve it with either the dashboard's "Approve" button
(if you can reach it via SSH tunnel) or the CLI:

```sh
./backup-maker pair accept <YOUR-DEVICE-ID>
```

## Reaching the dashboard on a headless Pi

The dashboard binds to `127.0.0.1` only and is never exposed to the network.
Reach it over an SSH tunnel from your desktop:

```sh
ssh -L 8666:127.0.0.1:8666 pi@raspberrypi.local
```

then open <http://127.0.0.1:8666> locally. (8666 is the default
`dashboard_port`.) Everyday health checks need no tunnel — just
`ssh pi@raspberrypi.local ./backup-maker status`.

## See also

- [hardware.md](hardware.md) — choosing the SSD, and
  avoiding counterfeit media.
- [Sleeping computers](sleeping-computers.md) — the problem an always-on Pi
  makes impossible.
- [Monitoring your backups](../guide/5-monitoring.md) — the status page this Pi can serve,
  and the read-only network view on the machine being backed up.
- [Getting started](../guide/1-install.md) — pointing your main machine at the
  destination you just built.
