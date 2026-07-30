# Screenshot harness

Regenerates every screenshot in `docs/screenshots`.

```sh
pip install playwright && playwright install chromium   # once

python3 tools/screenshots/shoot.py        # 07, 08, 09, mobile-dashboard
python3 tools/screenshots/shootwizard.py  # 01-06, 12, 13, mobile-wizard
python3 tools/screenshots/shootstatus.py  # 11-status-page
python3 tools/screenshots/lanview.py      # 10-network-view
```

That is every screenshot in `docs/screenshots`. Nothing is taken by hand.

They all write straight into `docs/screenshots`. Pass `--out <dir>` to look at the
results before replacing anything, which is the sensible way to run them.

## Why this exists

It has been written from scratch twice, because both previous copies lived in a
scratch directory and were thrown away with it. The screenshots are not
decoration: the README's tour is the first thing anybody sees, and between v0.1.9
and v0.1.11 it showed a dashboard nobody had — the state column said *"in sync"*
where the product says *"backed up"*, panels had been renamed, buttons had been
renamed, and the phone shot showed a sideways-scrolling table that had been
replaced by cards. None of that was noticed for two releases because re-shooting
was a job somebody had to rebuild the tools for first.

## The rule the harness exists to enforce

**A screenshot must never carry a real machine name, a real path, or a real
capacity.** `mockdash.py` invents a household — `/home/alex`, `sdcard`,
`//192.168.1.50/backups`, a paired `studio-pc`, 1.5TB of history — and serves it
to the real dashboard assets from `internal/webui/static`. Nothing about the
machine you run it on appears in the output.

## One shot is deliberately not a fixture

`10-network-view.png` is taken against a **real daemon** in a sandbox
(`lanview.py`), not from the fixture. That page's entire purpose is showing what
the read-only view does and does not expose, and a fixture would show whatever we
typed into it — so it could publish a promise the product does not keep. Instead
the script runs a real daemon over invented folders and asserts against the
rendered page that no path, no `/tmp/`, and no free-space figure appears before it
saves anything.

That has already paid for itself. Shooting it for real is what found the bug where
an idle folder went on reading *"checking for deleted files: 72,555"* for ever
(fixed in v0.1.11): six real rows sat there narrating a tidy-up that had finished.
A fixture has whatever phase you put in it and could never have shown it.

The trade is that this one is not bit-reproducible — it comes from a real engine,
so the "last sync" times differ run to run. The four fixture shots **are**
byte-identical between runs, so re-running `shoot.py` on an unchanged dashboard
produces no diff at all.

`lanview.py` builds the working tree, chooses free ports so a daemon you already
have running is untouched, keeps everything in a temp directory with its own
`XDG_CONFIG_HOME`, and cleans up after itself. It sets `lan_view_access = 'all'`
to skip the per-device approval gate, which would otherwise serve the holding page
instead of the view. It sets the sandbox up by calling the product's own
`init` / `add-target` / `add-folder`, rather than writing `config.toml` by hand,
because the drive markers and the recorded target UUIDs have to agree or the
daemon will correctly refuse to write to its own destinations.

## Three things that will otherwise waste an hour

- **`networkidle` never fires.** The dashboard holds an SSE connection open, so
  the network is never idle. Wait for `domcontentloaded`, then for a selector that
  only exists once the status has rendered.
- **On a phone the table rows are not *visible*.** The narrow layout stacks each
  row into a card and hides the header row, so Playwright's default visibility
  wait times out on a page that rendered perfectly well. Use `state="attached"`.
- **A full-page phone shot is ~10,000px tall.** Shots are clipped: the desktop
  ones to just below the running-total line, measured rather than hard-coded so a
  scenario with an extra row does not lose its last line.

## The wizard is driven, not faked

`shootwizard.py` clicks through the wizard the way somebody setting up a backup
would — kind, folder, destinations — against `wizardmock.py`, which serves the
endpoints those steps need: browse, machines, per-machine storage, and the adopt
scan and inspect. **`POST /api/backups` and `POST /api/adopt` are deliberately
absent**, so a script that ever pressed the final button gets a 404 rather than a
screenshot of something it claims to have done.

## The status page is rendered by the real Go code

`11-status-page.png` has no API to mock: it is one self-contained file produced by
`internal/statuspage`. So `statuspagehtml/main.go` calls the real `Render` with
invented data, and `shootstatus.py` opens the result over `file://` — which is
exactly how somebody reads it, off a share with no web server. If it ever needs a
server to look right, that is a bug in the page.

It is rendered **three days old on purpose**. The point of that shot is not "here
is a status page", it is the staleness banner: a page cheerfully reporting "backed
up" from a machine that died last week is false reassurance, and the script waits
for the banner to appear rather than assuming the markup is enough.

## Adding a scenario

Add a function to `mockdash.py`, register it in `SCENARIOS`, and add an entry to
`SHOTS` in `shoot.py`.

The shared guards are in `checks.py` and every script runs them: nothing local
leaked, the page really is showing the step the shot is for, and **no text is the
same colour as what is behind it**. That last one is there because all three of the
rendering faults found in one week were invisible to `go test` — dashboard states
drawn in the body colour, the wizard's question drawn as its own muted label, and
"Protect this folder" drawn as accent text on an accent background.

`shoot.py` additionally refuses to save a shot whose status table is empty, or in which any
state's computed colour is not the one its class promises — it reads `--ok`,
`--busy` and `--bad` out of the stylesheet's own variables and compares each
rendered state against them. That check exists because v0.1.10 shipped a dashboard
where every state was drawn in the body colour: all the words were right, nothing
errored, and a broken destination looked exactly like a healthy one for two
releases. Verified by breaking `style.css` on purpose — it refuses and writes
nothing.
