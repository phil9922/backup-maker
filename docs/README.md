# backup-maker documentation

Everything beyond the [project overview](../README.md), arranged roughly in the
order you'll need it: set it up, choose where the copies go, keep an eye on it,
and get your files back when something goes wrong.

## Getting started

- **[Getting started](getting-started.md)** — your first backup, making the
  daemon survive reboots, adding destinations, and what gets copied.

## Destinations

- **[Choosing destinations](destinations.md)** — the arrangement that covers
  the most failure modes for the least money, and how to size it.
- **[Hardware guidance](RECOMMENDED-HARDWARE.md)** — categories and principles
  rather than brands: always-on targets, offsite options, drives, formats.
- **[Setting up a Raspberry Pi as a backup target](RASPBERRY-PI-SETUP.md)** —
  from an empty microSD card to a Pi that receives backups around the clock,
  including everything that bit us on the way.

## Using it

- **[Scheduled snapshots](snapshots.md)** — encrypted point-in-time copies on a
  timer, alongside the live mirror.
- **[Monitoring your backups](monitoring.md)** — desktop alerts when backups
  stop working, the read-only network view, the status page written onto each
  destination, and the phone layout.
- **[When a destination fills up](space.md)** — what gets deleted to make room,
  what never does, and how to turn it on.
- **[Sleeping computers](sleeping-computers.md)** — why a sleeping destination
  silently stops receiving backups, and the three fixes including Wake-on-LAN.

## When things go wrong

- **[Restoring & recovery](recovery.md)** — getting a file back, rebuilding a
  machine from a destination, and repairing a backup someone edited.
- **[Security & safety properties](security.md)** — the guarantees, where
  credentials live, and why there is no off-site mode.

## Reference

- **[Reference](reference.md)** — config file locations, the daemon's
  architecture, building from source, and cutting a release.
