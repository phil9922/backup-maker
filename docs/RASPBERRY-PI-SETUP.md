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

## The steps still to come

This guide is written live against real hardware, and the reference Pi is
currently waiting on its SSD to arrive. The remaining steps land here as
they're actually performed and verified:

- **Step 3 — Test the SSD before trusting it** (`f3write`/`f3read`)
- **Step 4 — Format and mount the SSD** (ext4, by UUID, `nofail`)
- **Step 5 — Share it on the network** (Samba)
- **Step 6 — Point backup-maker at it** (the wizard, or `add-target share`)
- **Step 7 — Prove it works** (fill it up and watch reclamation behave)

Until then, [the README's Pi section](../README.md#pi-or-nas) covers the
same ground in less detail.
