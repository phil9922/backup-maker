# When a destination fills up

Every destination runs out of room eventually. This page covers what
backup-maker does by default (nothing — it tells you), how to let it reclaim
space instead, and the two things it will never delete to make room.

By default, nothing is ever deleted to make room — backup-maker just reports
that the destination is full. If you'd rather it kept itself tidy, set a
headroom figure:

```toml
[defaults]
min_free_gb = 5          # keep 5GB free everywhere
```

```toml
[[target]]
name = "sdcard"
min_free_gb = 2          # ...but only 2GB on the little SD card
```

Whatever you set here is shown on each destination's card ("keeping 5GB free"),
and its usage bar turns red as free space approaches the reserve — so the figure
isn't buried in a config file nobody reads.

When free space drops below that — or a write fails because the disk filled up
— backup-maker deletes the **oldest backup history** until there's room, then
carries on.

**What it will delete:**

- old file versions in `.backup-maker-versions/`
- old scheduled snapshots, **except the newest of each job**

**What it will never delete:**

- **the live mirror** — that *is* the backup. A destination missing files while
  still looking healthy is the worst possible failure, because you'd only find
  out during a restore.
- **a snapshot job's only remaining archive** — deleting it would leave that
  backup with no protection at all.

Deletion is spread **evenly across your folders**, oldest first, so one large
folder can't quietly consume every other folder's history. Every deletion is
logged and shown on the dashboard ("freed 2GB by deleting 14 old backup
files") — automatically removing your backup history should never be silent.

If there's nothing left that's safe to delete, the destination is marked
**full** and says so, rather than retrying forever. A destination too small to
hold even one copy of your folders can't be rescued by deleting history; that's
a hardware problem, and backup-maker will tell you so instead of thrashing.

Not available for paired-machine destinations: that machine runs its own
backup-maker and manages its own storage.

## See also

- [Choosing destinations](../guide/3-destinations.md) — sizing a destination before you
  buy it, so this page matters less.
- [Scheduled snapshots](../guide/4-snapshots.md) — retention counts, which prune snapshots
  independently of the headroom figure.
- [Reference](building.md) — where `config.toml` lives.
