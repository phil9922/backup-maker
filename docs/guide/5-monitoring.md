# 5. Monitoring your backups

The dashboard answers on `127.0.0.1` only, which is fine until you want to
check on things from a phone — or from anywhere at all while the computer being
backed up is asleep. This page covers the desktop alerts that come to you, the
read-only network view, the status page written onto each destination, and what
the dashboard looks like on a small screen.

## Being told when backups stop working

Everything else on this page waits to be looked at. A dashboard nobody opens
and a status page nobody browses to will happily hold a month-old failure. So
backup-maker also comes to you: when a backup stops working, it raises a
desktop notification.

```toml
[general]
desktop_alerts = true    # on by default
```

**These stay on screen until you dismiss them** (on desktops that honour
notification urgency — every mainstream Linux one does):

- a destination has been unreachable long enough to go **stale** (past
  `stale_after_days`)
- a destination is **full** and there is no old history left that may be deleted
- a **scheduled snapshot failed**
- **unrecognised storage** is mounted where a destination should be — a
  reformatted card, or a different drive at the same mount point. Nothing is
  written there, so this is the only way you would find out

**These appear and fade**, because they are good news rather than a problem:

- a destination **recovers** — it is reachable again, or has room again
- the **right storage is back** where unrecognised storage was
- a later run of a failed **snapshot succeeds**
- a machine is **asking to pair**

**Every sticky alert has an all-clear.** That is not a nicety: a notification
that stays on screen saying backups have stopped, with nothing ever to withdraw
it, sends you off to check by hand — which is the errand this is meant to save
you. So each of the first three alerts above is followed by its counterpart the
moment the fault clears, and each all-clear is sent once.

**Only changes are announced.** The alert fires when a destination crosses into
a bad state, and not again while it stays there — one alert per minute would
teach you to dismiss backup-maker without reading it, which is worse than not
alerting at all. Nothing is said about routine syncing, about space being
reclaimed, or about a destination merely being **offline**: an SD card is
unplugged and a NAS sleeps all the time, and only sustained failure is worth
interrupting somebody for.

A problem that was already there when the daemon started is announced once, on
its first cycle, so restarting is not a way to lose a warning.

**On a machine with no desktop this does nothing at all**, quietly — a
Raspberry Pi acting as a backup target has no screen to notify, and that is a
normal way to run backup-maker, not a fault. There is nothing to turn off.

Two honest limits. On **macOS** the notification is posted through
`osascript`, and whether it stays or fades is a per-app setting you own in
System Settings, not something backup-maker can ask for — so a critical alert
fades there like any other, though it is kept in Notification Center. On
**Windows** the toast is raised through Windows PowerShell (which is what lends
it an identity the shell will accept, so it is attributed to "Windows
PowerShell"), and stays about 25 seconds rather than for ever; missed ones are
in the Action Center.

### Changing any of this from the dashboard

The **Settings** panel at the bottom of the dashboard controls all of it —
nothing here needs the config file edited by hand, and every box takes effect
the moment you click it.

It holds two groups, **Alerts & Notifications** and **Security**, both **closed
until you open one**: these are settings you choose once and then leave for
months, so they stay out of the way of the page's real job, which is telling you
whether your backups are working. Click a heading to open it.

**One thing is deliberately visible while a group is shut:** if a delivery
method has stopped working, the *Alerts & Notifications* heading says so in red
without being opened. That is the one failure this program cannot announce by
alerting about it — the broken thing is the route an alert would travel — so it
is never allowed to hide behind a collapsed section.

- **How I am told.** The delivery methods, each with a **Test** button. Press it
  and a notification is sent right now, and the panel tells you whether it
  actually arrived. That matters more than it sounds: a working setup is silent
  by design, so without the test, "are alerts reaching me?" is only ever
  answered by a real failure. On a machine that cannot show notifications, the
  test says so instead of pretending.

  Under each method the panel says **how it last performed** — "Last delivered
  2m ago", or the daemon's own error if the attempt failed. This is the one
  fault backup-maker cannot report by alerting about it: if the route an alert
  would travel is broken, the alert never arrives to say so. A webhook address
  that was right last month and is dead today would otherwise be perfectly
  silent, and you would believe you were covered.

  A failing webhook or ntfy publish is also logged at **warning** level, unlike
  a desktop notification failing — which stays at debug, because a machine with
  no desktop (a Raspberry Pi target) is a normal way to run this program,
  whereas an address you configured yourself refusing the POST never is.

  **Methods are independent, not alternatives.** They are checkboxes: switch on
  as many as you want, and each is tried separately — a webhook pointing at a
  host that has gone away can never take your desktop notifications down with
  it.

  - **Desktop notifications** — a popup on this computer.
  - **Webhook** — the same alerts POSTed as JSON to an address you choose: a
    phone relay, a home-automation hub, a chat room. **Off by default**, and
    backups never need it. The payload is a small flat object:

    ```json
    {
      "source": "backup-maker",
      "level":  "critical",
      "title":  "card is stale",
      "body":   "Backups have not reached it for 3 days.",
      "machine": "my-laptop",
      "time":   "2026-07-27T07:34:22-04:00"
    }
    ```

    `level` is `critical` or `normal`, so a receiver that should only wake you
    for real problems needs to read one field.

    **"Don't include any detail"** replaces all of that with severity, a
    timestamp and the fixed sentence *"backup-maker needs attention"* — no
    machine name, no folder or destination names. Worth understanding rather
    than skipping: the last hop to a phone always crosses somebody else's
    server, and "backups to nas-attic have been stale for 3 days" describes
    your household to whoever runs it. Minimal mode still makes the phone buzz.

    The address is stored in `state.json`, not `config.toml`, because a webhook
    URL is usually a credential in its own right — a Slack or Discord endpoint
    is a right to post, an ntfy topic often carries a token. It is **never sent
    back to the dashboard**, so replacing it means typing a new one; saving an
    empty box removes it.

  - **ntfy** — the same alerts pushed to your phone through
    [ntfy](https://ntfy.sh), either the public server or one you host yourself.
    **Off by default**, and backups never need it.

    Set-up is three steps: install the ntfy app, subscribe it to a topic name
    nobody would guess, and paste that topic's address into the panel —
    `https://ntfy.sh/whatever-you-chose`. Press **Test** and the phone should
    buzz within a second or two.

    It is a **separate method from the webhook**, not a preset for it. ntfy has
    a publish format of its own, and going through it is what gets you a title,
    a priority the phone actually acts on, and an icon, instead of a page of
    JSON on a lock screen. A failure means "backups are not working" and is sent
    at ntfy's **max priority**, which is what breaks through Do Not Disturb if
    you have allowed it to; all-clears and pairing requests go at the ordinary
    priority, so ntfy stays worth leaving switched on.

    Alerts carry the machine's name in the **title** — `my-laptop: card is
    stale` — because the title is the line that survives truncation on a lock
    screen, and it is the whole alert if you run more than one machine.

    **"Don't include any detail"** works exactly as it does for the webhook, and
    matters more here: an ntfy topic name is **not a password**. Anyone who
    guesses or learns yours can subscribe to it and read everything sent there.
    Pick a topic name with some randomness in it, and if the content is
    sensitive, use minimal mode — or protect the topic and give backup-maker the
    access token.

    A **protected topic** (self-hosted, or an ntfy.sh account with access
    control) takes an access token, saved in its own box beside the topic. It is
    optional: public topics need none. If the server refuses the publish,
    backup-maker says so and says that a token is what is missing, rather than
    reporting a bare `403`.

    Self-hosted works, including behind a reverse proxy on a sub-path —
    `https://example.com/ntfy/alerts` publishes to `https://example.com/ntfy/`,
    not to the domain root. Credentials in the address
    (`https://user:pass@host/topic`) are carried as basic auth for proxies that
    expect that; an access token, if you set one, takes precedence.

    Both the topic and the token are stored in `state.json`, not `config.toml`
    — the topic for the reason above, the token for the obvious one. Neither is
    **ever sent back to the dashboard**, so each is replaced by typing a new one
    and removed by saving an empty box. They save separately, on purpose:
    because the browser is never told the token, one shared Save button would
    throw it away every time you edited the topic.

- **What I am told about.** One box per category: backups stopped reaching a
  destination, a snapshot failed, unrecognised storage, a computer asking to
  pair. **Switching one off silences its all-clear too**, deliberately, so you
  can never be left holding a sticky warning that nothing ever withdraws.
  Switching it back on re-announces a fault that is still happening, rather than
  comparing quietly against history you were never shown.

  In the config file these are:

  ```toml
  [general.alerts]
  snapshot_failed = false    # only what you switch OFF is written
  ```

  Categories you have not touched are absent from the file and default to on —
  which is what stops an upgrade silently switching off alerts nobody asked it
  to.

- **Let other devices on this network see backup status.** The read-only view
  described below. Switching it on starts the listener immediately and shows
  you the address; switching it off stops it a second later. If it is on but
  cannot listen — the port already taken, no network address — the panel says
  which, rather than guessing.

  Once it is on, choose **who can open it**:

  - **All devices on this network** — anyone who can reach the address, no
    password. The original behaviour, and still the default.
  - **Specific devices on this network** — a device that has never been here
    sees a holding page with a short code and nothing else, not even the
    machine name. The same code appears on your dashboard with **Approve** and
    **Deny**; approving it lets that device read backup status, and **Revoke**
    takes it away again immediately.

  Approval is remembered by a token in that browser's cookies, **not by IP
  address**. That is deliberate: a LAN address is a DHCP lease that moves, so
  it would lock out your own phone when the router reassigned it, and anyone
  already on the wifi could simply claim it — it would inconvenience you and
  stop nobody. The honest cost of the token is that clearing cookies, or
  opening the view in a different browser, asks again.

The panel is not shown on the read-only network view, which cannot change
anything and is not told what you have switched off.

## Bookmarking the dashboard

`http://127.0.0.1:<dashboard_port>` — 8666 unless you changed it — is a
permanent address. `127.0.0.1` always means "this computer", so unlike a LAN
address it survives changing networks, moving house, and a new router. And if
the port is ever taken by something else, the daemon refuses to start and says
so rather than quietly moving, so the address cannot drift.

Sign in **once** with `backup-maker web`, which exchanges your token for a
session cookie, then bookmark the **plain** URL. You should not need to run it
again: the session survives closing the browser, lasts a year, and every visit
pushes that back another year.

There is no shorter expiry on purpose. The cookie holds the same token that
already sits unexpiring in `state.json`, so timing it out raises nobody's
difficulty — anyone who can read your browser profile can read that file, as
the same user on the same machine. All a short window would achieve is a day
when your bookmark quietly stops working.

Don't bookmark the `…/auth?token=…` URL that `backup-maker web` opens. It works,
but it puts a live credential into a bookmark — and browser bookmark sync would
carry it off the machine, which is exactly what that token existing on
loopback-only is meant to prevent.

### Which version am I running?

The dashboard shows the running build quietly in its footer, so "does this
machine have that fix yet?" doesn't need a terminal. A release shows as
`v0.1.2`; a binary you compiled yourself shows `dev`, and `dev-dirty` if the
working tree had uncommitted changes — both are saying "this is not a release",
which is worth seeing plainly. `backup-maker version` gives the same answer
with the commit and build date.

The **network view does not show it**. An exact version tells whoever reads it
which known bugs apply, and that view has no password — the same reasoning that
strips drive capacities and paths from it.

## Watching from another device on your network

By default the dashboard answers only on this computer. If you'd like to check
backup progress from a phone or another PC, turn on the read-only network view:

```toml
[general]
lan_view = true          # off by default
lan_view_port = 8667     # must differ from dashboard_port
```

The daemon then logs the address to open, along with the MAC to reserve it
against on your router:

```
read-only network view listening url=http://192.168.1.20:8667
  interface=eth0 mac=aa:bb:cc:dd:ee:ff
  note="reserve this address on your router to keep the URL stable"
```

**It is genuinely read-only.** Setting up, changing or removing a backup —
and browsing the filesystem — remain possible only on the computer itself.
That isn't a UI convention: the network view is a separate listener with an
**allow-list** of routes, so anything not explicitly permitted is refused,
including routes added in future versions. Authenticating doesn't change it;
a valid token still gets `403` on anything that writes. The token exchange
(`/auth`) isn't served there at all, so your token never crosses the network.

**No password needed, and no paths shown.** Any device on your network can open
it and see whether backups are working. What it deliberately does *not* show is
the shape of your machine: folder **labels** appear ("code", "photos") but not
their paths, destinations appear by **name** but not their addresses, and the
device ID and receive folder are omitted. "Are my backups working?" is
reasonable to publish to your wifi; "here is my directory layout and where my
NAS lives" is not.

The page notices it's the read-only view and hides the controls it can't use,
rather than showing buttons that fail when tapped:

![The dashboard as another device sees it: a read-only banner, folder labels and destination names with no paths, and no setup or remove controls](../screenshots/10-network-view.png)

Two things worth knowing:

- **An application can't reserve an IP address.** Your machine's address comes
  from your router, and a service can only bind an address the host already
  has. To stop the URL changing, set a **DHCP reservation** on the router using
  the MAC shown above. And nothing can serve this view while the computer is
  off — there's no software running to answer.

## Checking backups when your computer is off

The dashboard is served by the computer being backed up, so it goes dark
exactly when you most want it — when that machine is asleep, broken or stolen.

So backup-maker also writes a small **status page onto each destination**,
beside the backups:

```
/mnt/backups/
  ├─ backup-maker-status.html          ← open this from any device
  ├─ workstation/
  │    ├─ backup-maker-status.html     ← that computer's own report
  │    └─ code/…
  ├─ laptop/
  │    ├─ backup-maker-status.html
  │    └─ documents/…
  └─ backup-maker-archives/…
```

Each computer writes its own page inside its own folder, and the one at the top
lists them all — so a destination shared by two computers reports on both
instead of showing whichever wrote last. **Open the top one**; it links to the
rest, and it is built from what is actually on the storage rather than from what
any one computer believes.

They're single self-contained files — no web server needed. Browse the share
from a phone or another PC and open it. If that destination is a Pi or NAS with
a web server, point it at that folder and the page is a URL anyone on your
network can visit, whether or not your computer is running.

**It refuses to pretend it's current.** The page leads with *"last reported 4
minutes ago"*, recomputed in your browser each time you open it. Past an hour it
stops presenting itself as status at all:

![The status page written to a destination, shown three days stale: a red banner reading "This page is out of date — treat it as history, not status"](../screenshots/11-status-page.png)

That warning is the whole point. A page cheerfully reporting "all in sync" from
a machine that died last week is worse than no page: it is false reassurance,
and it would be discovered during a restore. Knowing a machine **stopped**
backing up three days ago is the single most valuable thing this can tell you.

Like the network view, it carries folder **labels** and destination **names**
only — never paths or addresses.

### Serving the status page over HTTP

If your destination is a Pi or NAS that stays powered, you can make the status
page available as a URL on your network. Either `python3` or nginx will do it.
The page is refreshed once a minute by backup-maker, so it's as current as the
last sync cycle.

**Serve a folder containing only the status page — never the destination
itself.** Pointed at the destination root, a plain file server would hand out
your entire backup: the file names, and the files. So give it a directory of
its own with a link to the page inside it:

```sh
sudo mkdir -p /var/www/backup-status
sudo ln -sf /mnt/backups/backup-maker-status.html /var/www/backup-status/index.html
```

(Replace `/mnt/backups` with your destination path. The link is followed on
every request, so the page stays current as backup-maker rewrites it.)

**Tradeoffs:** this is plain HTTP with no authentication, so any device on your
network can read your backup's health — which folders you protect, and whether
they're in sync. `python3 -m http.server` is single-threaded and written for
development, not hostile networks; it's fine on a home LAN and that's the scope
here. The page is only as fresh as the last write: if your computer hasn't run
in a while, the page says so itself rather than looking current.

**Option 1: systemd unit with Python's built-in server**

```sh
sudo tee /etc/systemd/system/backup-status.service >/dev/null <<'EOF'
[Unit]
Description=Serve backup-maker status page over HTTP
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
ExecStart=python3 -m http.server 8668 --bind 192.168.1.20 --directory /var/www/backup-status
Restart=on-failure
RestartSec=30s
User=pi

[Install]
WantedBy=multi-user.target
EOF
sudo systemctl enable --now backup-status.service
```

(Replace `192.168.1.20` with your Pi's LAN address. Binding to that address
rather than `0.0.0.0` keeps the page off any other network the box is on. Port
8668 is arbitrary; use any unused port above 1024.)

Open <http://192.168.1.20:8668/> from any device on your network.

**Option 2: nginx server block**

If you already run nginx on the destination, add a server block to its config
(usually `/etc/nginx/sites-available/default` or a file in `/etc/nginx/conf.d/`):

```nginx
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
```

(Replace `192.168.1.20` with your LAN address and `8668` with your chosen port.)

Reload nginx:

```sh
sudo nginx -s reload
```

Open <http://192.168.1.20:8668/> from any device on your network.

## On a small screen

The dashboard adapts to narrow windows and phone-sized screens — the status
table scrolls sideways rather than squashing its columns.

Worth being straight about how you'd get there, though: the dashboard listens
on `127.0.0.1` only, so **a phone on your network cannot simply open it**.
Reaching it from a phone means running an SSH client app and forwarding the
port, which is genuinely fiddly:

```sh
ssh -L 8666:127.0.0.1:8666 you@the-machine
```

then opening <http://127.0.0.1:8666> on the phone. In practice this layout
earns its keep more often for a narrow browser window, a split screen, or a
tablet — and for checking a headless machine you're already SSH'd into.

**For a phone, the network view above is the answer**, not the dashboard.
Open `http://<that machine's address>:8667` and use your browser's *Add to
Home Screen* — Share on iOS, the ⋮ menu on Android — and checking your backups
becomes one tap. Opened on a phone-sized screen, the page reminds you how.

Two things to know before you do. It is the **read-only** view: you can watch,
not change. And **the address must stop moving** — it is your machine's DHCP
lease, so reserve it in your router, or the icon will point at nothing the
next time the lease changes.

| Setting up | Watching progress |
| --- | --- |
| ![The setup wizard on a phone, showing the computer list with storage ticked](../screenshots/mobile-wizard.png) | ![The dashboard on a phone, showing progress bars and the folders panel](../screenshots/mobile-dashboard.png) |

Prefer the command line? Everything the dashboard does has a CLI equivalent —
see [Getting started](1-install.md), and use `backup-maker wizard` for
the terminal version.

## See also

- [Setting up a Raspberry Pi as a backup target](../setup/raspberry-pi.md) — has a
  tested nginx recipe for the status page on a Pi.
- [Sleeping computers](../setup/sleeping-computers.md) — the most common reason a
  destination stops receiving without you noticing.
- [Security & safety properties](../reference/security.md) — what the network view and the
  status page deliberately never publish.
