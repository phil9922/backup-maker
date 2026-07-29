# backup-maker documentation

**New here? Read [the guide](guide/) in order — it starts at
[1. Installing backup-maker](guide/1-install.md).** Six short pages take you
from an unpacked binary to backups you can prove are working. Everything else
on this page is for dipping into once you have that.

You can also read all of this **inside backup-maker itself, with no internet
connection**: open the dashboard and click **Docs**. These pages are built into
the binary.

---

## The guide — read in order

| Page | What it covers | |
|---|---|---|
| **[1. Installing backup-maker](guide/1-install.md)** | Getting the binary onto a machine and making it survive a reboot. | 10 min |
| **[2. Your first backup](guide/2-first-backup.md)** | Choosing what to protect, adding a destination, and what actually gets copied. | 10 min |
| **[3. Choosing destinations](guide/3-destinations.md)** | Which arrangement of drives and machines covers the most failure modes for the least money. | 5 min |
| **[4. Timed snapshots](guide/4-snapshots.md)** | Encrypted point-in-time copies on a timer, alongside the live mirror. | 5 min |
| **[5. Monitoring your backups](guide/5-monitoring.md)** | Alerts when backups stop, delivery to your phone, the read-only network view, and the status page written onto each destination. | 15 min |
| **[6. Restoring & recovery](guide/6-restoring.md)** | Getting a file back, rebuilding a machine from a destination, repairing a backup someone edited. | 10 min |

## Drives

| Page | What it covers | |
|---|---|---|
| **[Preparing a new drive](guide/preparing-a-drive.md)** | A drive out of the box has no filesystem and cannot be used until it has one. Formatting it, mounting it so it comes back after a reboot, and getting one ready for a Pi or NAS on a different computer. | 10 min |
| **[My drive doesn't show up](guide/troubleshooting-drives.md)** | A drive you plugged in isn't offered: blank, unmounted, read-only, or in a computer this one can't reach. | 5 min |

## Setting up specific hardware

Read these when they apply to you, not before.

- **[Raspberry Pi as a backup target](setup/raspberry-pi.md)** — from an empty
  microSD card to a Pi that receives backups around the clock, including
  everything that bit us on the way.
- **[Hardware guidance](setup/hardware.md)** — categories and principles rather
  than brands: always-on targets, offsite options, drives, formats.
- **[Sleeping computers](setup/sleeping-computers.md)** — why a sleeping
  destination silently stops receiving backups, and the three fixes including
  Wake-on-LAN.

## Reference

- **[Security & safety properties](reference/security.md)** — the guarantees,
  where every credential lives, and exactly what can leave your network.
- **[Config files and where things live](reference/config.md)** — which file is
  safe to share and which one never is.
- **[When a destination fills up](reference/space.md)** — what gets deleted to
  make room, what never does, and how to turn it on.
- **[Building from source](reference/building.md)** — the architecture,
  compiling it yourself, and how a release is cut.

---

Looking for what backup-maker *is*, rather than how to use it? That is the
[project overview](../README.md).
