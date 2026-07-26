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

## What you need

- **A Raspberry Pi** — this guide uses a Pi 5, but any 64-bit-capable model
  (Pi 3 onward) works. Check [Building for the Pi](../README.md#building-for-the-pi)
  in the README for the OS-architecture details.
- **The official power supply, or one genuinely rated 5V/5A** (Pi 5). This is
  not pedantry: a weaker supply makes the Pi clamp its USB ports to 600mA, and
  a bus-powered SSD then browns out mid-write. That failure *looks like a
  dying disk* — you will debug the wrong thing.
- **A microSD card for booting** — 16GB is plenty. The OS lives here and
  *nothing else does*; backups never touch this card, because microSD cards
  wear out under constant writing.
- **An external SSD in a USB enclosure** — this holds the backups. Size it at
  roughly 2× the data you're protecting; versioning keeps ~30 days of file
  history. See [RECOMMENDED-HARDWARE.md](RECOMMENDED-HARDWARE.md) for how to
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
   [Building for the Pi](../README.md#building-for-the-pi)).
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

## Steps 3 and 4 — test, format and mount the SSD

> Not yet written. The reference Pi is still waiting for its drive, and this
> guide only describes steps that have actually been carried out. These will
> cover verifying the SSD is genuine with `f3write`/`f3read`, formatting it
> ext4, and mounting it at `/mnt/backups` **by UUID with `nofail`** so a
> missing drive doesn't leave the Pi unbootable.

Steps 5 and 6 below are written and tested — they were set up against the
mount point in advance, so when the drive arrives it only has to be mounted.

## Step 5 — Share the drive on the network

This is what lets backup-maker treat the Pi as a network drive, with nothing
installed on it beyond Samba.

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

backup-maker writes a small `backup-maker-status.html` to each destination,
so you can check your backups' health from a phone even while your computer
is off. Serving it needs care: **never point a web server at the backup
root** — it would hand out the backups themselves. Give it a directory
containing nothing but a link to the page:

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

In the meantime, [the README's Pi section](../README.md#pi-or-nas) covers the
same ground in less detail.
