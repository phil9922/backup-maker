# Config files and where things live

Where backup-maker keeps its settings and its private state, and which file is
safe to share.

## Config

`~/.config/backup-maker/config.toml` (Linux) / `%AppData%\backup-maker`
(Windows) / `~/Library/Application Support/backup-maker` (macOS). Hand-edit
freely — the running daemon picks changes up within seconds.

## The two files

- **`config.toml`** — everything you can safely share or copy between machines:
  folders, destinations, ports, and the `[general]` settings the dashboard's
  Settings panel edits. Hand-edit it freely; a running daemon picks changes up
  within seconds.
- **`state.json`** (mode 0600) — machine-owned and private: share passwords,
  archive passwords, the webhook address, the ntfy topic and token, and the
  dashboard's API token. **Never share this file**, and never commit it.

Which `[general]` keys exist is documented alongside the features that use
them — see [5. Monitoring](../guide/5-monitoring.md) for alerts, delivery and
the network view, [When a destination fills up](space.md) for `min_free_gb`,
and [6. Restoring](../guide/6-restoring.md) for `versioning_max_age_days`.

---

See also: **[Security & safety properties](security.md)** — what is guaranteed,
and where every credential lives.
