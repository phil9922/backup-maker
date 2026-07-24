# Hardware guidance (vendor-neutral)

backup-maker works with whatever storage you already have. When you *do* buy
something, this page describes **categories and principles only** — no brands,
no models, no purchase links. Anything sold under many brands works if it
meets the description.

## Local always-on target

The most valuable backup target is one that's powered on whenever you're
working, so changes mirror within seconds:

- **A drive that lives in/on the computer permanently** — a memory card left
  in the card slot, a small USB stick, or an internal second drive. Cheap and
  zero-effort, but it shares the computer's fate (theft, surge, fire), so it
  must never be the *only* copy.
- **A single-board computer (e.g. a Raspberry Pi) with an external SSD** — the
  cheapest always-on option: a few watts, silent, and with no sleep mode to
  fall into. Either share the drive over SMB (nothing else to install) or run
  backup-maker on it and pair. Back up to the external drive, never the
  boot card: cards wear out under constant writing.
- **A NAS (network-attached storage) box** — always on, reachable by every
  machine in the house, and usable by backup-maker as a network-drive target
  over SMB. Two-bay models can mirror internally for extra safety.
- **A router with a USB port** — many home routers can share an attached USB
  drive over the network (enable SMB in the router's settings). Slower than a
  NAS, but effectively free if your router supports it.
- **Any spare computer that stays on** — install backup-maker on it and use
  machine-to-machine pairing, the most robust target type.

## Offsite options

Every copy in one building can be lost together. In rough order of protection:

1. **A rotated drive** — two identical external drives; one connected at
   home, one stored elsewhere; swap them on a schedule you'll actually keep
   (calendar reminder helps). Works with no network setup at all, and an
   encrypted rotated drive is the strongest choice for highly sensitive data
   because it never crosses any network.

backup-maker is local-network only by design — it has no off-site or
cloud mode — so carrying a drive somewhere else is how you get a copy that
survives the building. A machine at another location can only help if it sits
on the same network as this one (for example over your own VPN).

## Choosing drives and cards

- **Match capacity to your data with headroom**: versioning keeps ~30 days of
  file history, so buy at least 2× your current data size.
- **For always-inserted cards/sticks**, look for ones rated "high endurance"
  (designed for continuous writing, e.g. dashcam/surveillance-rated) — normal
  cards wear out faster under constant small writes.
- **Buy from an authorized seller.** Counterfeit memory cards and USB sticks
  that report a false, larger capacity are rampant in gray-market listings;
  they silently corrupt data past their real size — the worst possible trait
  in backup hardware. If a price looks too good, it is.
- **Solid-state vs spinning**: SSDs tolerate being knocked around (good for
  rotated offsite drives); traditional spinning drives give the most capacity
  per dollar for a stay-at-home NAS.
- **Test new media before trusting it**: fill it once completely and read it
  back (any free "verify full capacity" approach), then let backup-maker's
  read-back verification guard it day to day.

## Filesystem / format notes

- **exFAT** works everywhere (Linux, Windows, macOS) and has no 4GB file
  limit — the safest choice for drives that may move between machines.
- **FAT32** also works everywhere but cannot hold any single file over 4GB;
  backup-maker will flag such files rather than fail the whole backup.
- **NTFS** (Windows-native) and **ext4** (Linux-native) are fine when the
  drive stays with one OS.
- Whatever the format, enable your **operating system's built-in drive
  encryption** for backups of anything you'd mind losing control of — every
  major OS ships this for free.

## What you don't need

No subscription, no cloud account, and no specialized "backup appliance" —
backup-maker's whole design is that ordinary storage you own and control is
enough.
