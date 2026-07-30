# backup-watch — a dead-man's switch for the destination

Runs **on the always-on destination**, not on the machine being backed up. It
watches for silence: if `backup-maker-status.html` stops being updated, the
machine that writes it has stopped backing up, whatever the reason — powered off,
crashed, daemon hung, network down, drive unplugged, misconfigured.

**Why it matters more than anything running on the laptop.** A machine cannot
report its own death. Every check that runs on the computer being backed up fails
in exactly the case that matters. This is the only layer that survives that
computer dying, which is also why its alerts are the ones that most need
somewhere to go.

## Why it is in the repo

It was not. It existed only as a root-owned script on one Raspberry Pi, mentioned
in a handoff note. That is the same way the screenshot harness got lost twice in a
week. If the Pi is reimaged, this is gone and nobody knows it was ever there.

## Install

```sh
sudo install -o root -g root -m 755 backup-watch /usr/local/bin/backup-watch
```

with a `oneshot` service and a 15-minute timer. The alert endpoint belongs in a
root-only environment file rather than in the unit — on ntfy.sh the topic name is
the access control, so it is a credential, and anything written into the unit is
readable by every user through `systemctl cat`:

```sh
printf 'BACKUP_WATCH_WEBHOOK=%s\n' "$URL" | sudo tee /etc/backup-watch.env >/dev/null
sudo chmod 600 /etc/backup-watch.env
sudo mkdir -p /etc/systemd/system/backup-watch.service.d
printf '[Service]\nEnvironmentFile=/etc/backup-watch.env\n' \
  | sudo tee /etc/systemd/system/backup-watch.service.d/webhook.conf >/dev/null
sudo systemctl daemon-reload
```

## Settings

| Variable | Default | What it does |
|---|---|---|
| `BACKUP_WATCH_ROOT` | `/mnt/backups` | where the destination is mounted |
| `BACKUP_WATCH_MAX_AGE_MIN` | `360` | how stale a status page may get before it complains |
| `BACKUP_WATCH_STATE` | `/var/lib/backup-watch` | where the last state is remembered |
| `BACKUP_WATCH_WEBHOOK` | *(unset)* | POSTed on a state change; unset means log only |
| `BACKUP_WATCH_WEBHOOK_STYLE` | sniffed | `ntfy` or `json`; sniffed from the URL |

It alerts only on a **change** of state, so a machine that stays dead does not
notify every fifteen minutes. It is deliberately silent when the destination is
not mounted, and when no status page has ever appeared — neither is a failure.

## THE COUPLING TO WATCH

`BACKUP_WATCH_MAX_AGE_MIN` is a threshold on how often backup-maker rewrites that
page, and **that cadence changed in v0.1.12**. It used to be every single minute;
it is now written when its contents change, plus a heartbeat at least every 15
minutes so the timestamp keeps moving (`statusHeartbeat` in
`internal/daemon/statuswriter.go`).

So the threshold must stay well clear of 15 minutes or a perfectly healthy machine
gets reported dead. The default 360 leaves a 24× margin. **Do not lower it near 15
without reading `statusHeartbeat` first**, and if that heartbeat is ever
lengthened, this is the thing that breaks — in the direction of a false alarm
about backups having stopped, which is the most expensive kind of wrong this
project can be.

## Testing it without crying wolf

Point the webhook at a local listener and force a stale verdict, so the
priority-5 payload can be inspected without a false alarm reaching a phone. Use a
throwaway state directory so the live one is untouched:

```sh
T=$(mktemp -d)
sudo env BACKUP_WATCH_WEBHOOK=http://127.0.0.1:8791/ntfy BACKUP_WATCH_STATE="$T" \
     BACKUP_WATCH_MAX_AGE_MIN=0 /usr/local/bin/backup-watch
```

Note that it still writes a line to syslog via `logger`, so a test leaves a
scary-looking `BACKUPS HAVE STOPPED` entry in the journal. That is the test, not
the thing.
