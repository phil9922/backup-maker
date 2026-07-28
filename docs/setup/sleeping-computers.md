# Sleeping computers

Read this if your backup drive is plugged into a PC, or *is* a PC. A sleeping
destination is the most common way people end up with a backup they believe is
current and isn't — and unlike most failures, it produces no error at the time.
This page explains what happens while a destination sleeps, the three fixes in
order of how much you can rely on them, and how to set up Wake-on-LAN if you
need the last of them.

If your backup drive is plugged into a computer — or *is* a computer, like a
NAS or a paired machine — and that computer is asleep, hibernating, or off,
then that target is unreachable and **nothing is backed up to it while it
sleeps.** Simply trying to reach a sleeping PC's shared drive does not wake
it.

This is the most common way people end up with a backup they *think* is
current and isn't.

backup-maker can *try* to wake such a machine with Wake-on-LAN (see
[Fix 3](#fix-3-wake-on-lan--wake-the-machine-on-demand)), but that is a
best-effort rescue with real prerequisites — not a guarantee, and not a
substitute for a target that's simply always on.

> ### Recommendation
>
> **1. Plug an external SSD into a Raspberry Pi and use that as your backup
> target.** A Pi draws a few watts, makes no noise, and *never sleeps* — a
> stock Raspberry Pi OS install has no suspend mode to fall into, so the
> problem on this page cannot happen. It costs less than most external
> drives. Setup is in the
> [Raspberry Pi guide](raspberry-pi.md).
>
> **2. A NAS**, if it's configured not to sleep. Most NAS boxes stay awake by
> default but ship with a "disk hibernation" or "HDD standby" option, and
> some vendors enable a deeper system sleep — check yours and turn it off.
> More capacity and internal redundancy than a Pi, at several times the
> price. See [hardware.md](hardware.md).
>
> **3. An ordinary computer you have deliberately set to never sleep** (Fix 2
> below) is equally safe. The risk isn't using a PC — it's using a PC that
> still sleeps.
>
> Wake-on-LAN (Fix 3) is the fallback for when none of those is an option.

What actually happens while the target is asleep:

- Nothing is lost on your source machine — changes keep being tracked.
- The target pauses cleanly and shows `offline` in `backup-maker status`.
- When the machine wakes, that target catches up exactly, without recopying
  everything.
- After 7 days unseen, status flags it stale (`!!`). **Check your status
  regularly** — a target that only wakes when you happen to use that computer
  can sit hours or days behind.

Note the difference between two things that sound alike:

- **The drive spinning down** (an idle external disk parking its heads) is
  harmless — it wakes automatically on the next access, and backup-maker
  never notices.
- **The host computer sleeping** stops everything, because the USB port, the
  network card, and the file-sharing service are all powered down with it.

## Fix 1 (best): use a target that never sleeps

Move the drive to something built to stay on — a Raspberry Pi, a NAS, or a
router USB port. This is the only fix that needs no ongoing attention and
can't be undone by a future Windows update resetting your power settings.

## Fix 2: stop the host computer from sleeping

Keep the machine awake and it will serve the drive continuously. Screen-off
is fine; it's *system* sleep you need to disable.

**Windows** — Settings → System → Power & battery → Screen and sleep → set
**"When plugged in, put my device to sleep after" = Never**. Then, because
two other settings can still cut off access:

```
powercfg /change standby-timeout-ac 0     # never sleep on AC
powercfg /change hibernate-timeout-ac 0   # never hibernate on AC
```

Also open Device Manager → Network adapters → your adapter → Properties →
Power Management and **uncheck "Allow the computer to turn off this device to
save power"**, and do the same for **USB Root Hub** entries if the drive drops
off. On a laptop, Control Panel → Power Options → "Choose what closing the lid
does" → set **"Do nothing"** when plugged in.

**macOS** — System Settings → Displays → Advanced → enable **"Prevent
automatic sleeping on power adapter when the display is off"**, or from the
terminal:

```sh
sudo pmset -a sleep 0 disksleep 0
```

macOS is the one platform where sleep can genuinely be survivable: enable
**"Wake for network access"** (`sudo pmset -a womp 1`), and on Apple hardware
with a Bonjour Sleep Proxy on the network, an incoming file-sharing request
can wake the Mac. It's convenient but not something to bet your only backup
on — the first write after wake can still time out.

**Linux** — mask the sleep targets outright:

```sh
sudo systemctl mask sleep.target suspend.target hibernate.target hybrid-sleep.target
```

(and turn off automatic suspend in your desktop's power settings).

## Fix 3: Wake-on-LAN — wake the machine on demand

backup-maker can broadcast a Wake-on-LAN "magic packet" to a sleeping target.
Give a target the MAC address of the machine behind it and, whenever that
target is offline, the daemon tries to wake it (at most once every 5 minutes,
so it never floods your network):

```sh
backup-maker set-mac <target> aa:bb:cc:dd:ee:ff   # enable
backup-maker wake <target>                        # test it now
backup-maker set-mac <target> none                # disable
```

You can also set it when creating the target:

```sh
backup-maker add-target share //NAS/backups --mac aa:bb:cc:dd:ee:ff
backup-maker add-target device <DEVICE-ID> --mac aa:bb:cc:dd:ee:ff
```

Read the honest limits first:

- **It is best-effort and unacknowledged.** A magic packet is fire-and-forget
  UDP. `backup-maker wake` succeeding means "the packet left this machine",
  never "the target is awake".
- **Wifi almost never works.** Most wifi adapters lose power on sleep and stop
  listening. Treat WoL as an **ethernet-only** feature.
- **It won't cross subnets or the internet.** The packet is a LAN broadcast —
  consistent with backup-maker being local-only, and it cannot reach a machine
  outside your network.
- **The machine will go back to sleep** on its own timer, possibly mid-copy.
  A large first backup may need several wake cycles to finish.
- **It usually won't work from a full power-off** unless the BIOS explicitly
  supports waking from S5 ("Power On By PCI-E"). Sleep and hibernate are the
  reliable cases.

Because of all that: WoL makes a sleeping target *much* better than nothing,
but a target that never sleeps is still strictly better.

### Step 1: find the MAC address (on the target machine)

Use the **wired ethernet** adapter's address:

| OS | Command |
| --- | --- |
| Linux | `ip link` — the `link/ether` line under your ethernet interface |
| macOS | `ifconfig en0 \| grep ether` |
| Windows | `getmac /v` (or `ipconfig /all` → "Physical Address") |

### Step 2: enable Wake-on-LAN in the target's BIOS/UEFI

This is the step people skip, and nothing else works without it. Reboot into
BIOS/UEFI setup (usually <kbd>Del</kbd>, <kbd>F2</kbd>, or <kbd>F10</kbd> at
power-on) and enable whichever of these your board calls it:

- "Wake on LAN" / "Wake on PCI-E" / "Power On By PCI-E"
- "Resume by PCI-E Device" / "PME Event Wake Up"

On many Windows machines you must **also disable "Fast Startup"** (see below),
because it makes shutdown a hibernation state that ignores wake events.

### Step 3: enable it in the target's OS

**Windows**

Device Manager → Network adapters → your **wired** adapter → Properties:

- *Power Management* tab → tick **"Allow this device to wake the computer"**
  and **"Only allow a magic packet to wake the computer"**. Leave "Allow the
  computer to turn off this device to save power" **unticked**.
- *Advanced* tab → set **"Wake on Magic Packet"** to *Enabled*. If present,
  also enable "Wake on pattern match" and set "Energy Efficient Ethernet" to
  *Disabled* (it can drop the link on idle).

Then disable Fast Startup, which otherwise breaks waking after shutdown:

```
powercfg /hibernate off        # simplest, also frees disk space
```

or leave hibernate on and untick Control Panel → Power Options → "Choose what
the power buttons do" → "Turn on fast startup".

**macOS**

```sh
sudo pmset -a womp 1     # "wake on magic packet"
pmset -g | grep womp     # verify: should print 1
```

On Apple laptops this only applies while on power; on wifi it depends on a
Bonjour Sleep Proxy being present and is unreliable. Ethernet is dependable.

**Linux**

Check what the adapter supports (`g` = wake on magic packet):

```sh
sudo ethtool eth0 | grep -i wake     # "Supports Wake-on: pumbg" / "Wake-on: d"
sudo ethtool -s eth0 wol g           # enable ("d" = disabled)
```

That resets on reboot. Make it stick with a systemd unit:

```sh
sudo tee /etc/systemd/system/wol.service >/dev/null <<'EOF'
[Unit]
Description=Enable Wake-on-LAN
After=network-online.target

[Service]
Type=oneshot
ExecStart=/usr/sbin/ethtool -s eth0 wol g

[Install]
WantedBy=multi-user.target
EOF
sudo systemctl enable --now wol.service
```

(Replace `eth0` with your interface name from `ip link`. On NetworkManager
you can instead run `nmcli connection modify <name> 802-3-ethernet.wake-on-lan
magic`.)

### Step 4: test it

Put the target to sleep, then from the backup-maker machine:

```sh
backup-maker wake <target>
backup-maker status          # the target should return within a minute
```

If nothing happens, work backwards: BIOS setting, then the OS setting, then
whether you used the wired adapter's MAC. If your machines are on different
subnets or a VLAN, point the packet at the right broadcast address:

```sh
backup-maker set-mac <target> aa:bb:cc:dd:ee:ff --broadcast 192.168.1.255
```

## See also

- [Setting up a Raspberry Pi as a backup target](raspberry-pi.md) — Fix 1,
  built step by step.
- [hardware.md](hardware.md) — always-on target
  categories, including NAS boxes.
- [Monitoring your backups](../guide/5-monitoring.md) — how to notice a target that has
  gone quiet, including from another device.
