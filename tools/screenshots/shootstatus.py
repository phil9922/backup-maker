#!/usr/bin/env python3
"""Take 11-status-page.png — the page written onto each destination.

    python3 tools/screenshots/shootstatus.py
    python3 tools/screenshots/shootstatus.py --out /tmp/shots --fresh

Renders the page with the real internal/statuspage code (see
statuspagehtml/main.go) and opens the result over file://, which is exactly how
somebody reads it: one self-contained file, off a share, with no web server and no
network. If this ever needs a server to look right, that is a bug in the page.

WHAT THE SHOT IS FOR. Not "here is a status page" — the staleness banner. A page
cheerfully reporting "backed up" from a machine that died last week is false
reassurance, which is the one thing a backup tool must never give, and this page
refuses to give it: past an hour it stops presenting itself as status at all. The
banner is drawn in the reader's browser from the written-at stamp, so the page is
rendered three days old to bring it out.
"""
import argparse
import os
import subprocess
import sys
import tempfile

import checks
import mockdash
from playwright.sync_api import sync_playwright

REPO = mockdash.REPO
DEFAULT_OUT = os.path.join(REPO, "docs", "screenshots", "11-status-page.png")
WIDTH = 1500


def render(fresh):
    go = subprocess.run(["which", "go"], capture_output=True, text=True).stdout.strip() \
        or os.path.expanduser("~/.local/share/go-toolchain/bin/go")
    cmd = [go, "run", "./tools/screenshots/statuspagehtml"]
    if fresh:
        cmd.append("-fresh")
    p = subprocess.run(cmd, cwd=REPO, capture_output=True)
    if p.returncode != 0:
        sys.exit("rendering the page failed:\n" + p.stderr.decode())
    return p.stdout


def main():
    ap = argparse.ArgumentParser(description=__doc__,
                                 formatter_class=argparse.RawDescriptionHelpFormatter)
    ap.add_argument("--out", default=DEFAULT_OUT)
    ap.add_argument("--fresh", action="store_true",
                    help="shoot it current, with no staleness banner")
    a = ap.parse_args()

    html = render(a.fresh)
    with tempfile.TemporaryDirectory(prefix="bm-statuspage-") as d:
        path = os.path.join(d, "backup-maker-status.html")
        with open(path, "wb") as f:
            f.write(html)
        with sync_playwright() as p:
            b = p.chromium.launch()
            page = b.new_page(viewport={"width": WIDTH, "height": 900})
            errors = []
            page.on("pageerror", lambda e: errors.append(str(e)))
            page.goto("file://" + path, wait_until="load")
            # The banner is computed by the page's own script. Waiting for it is
            # what proves the mechanism works, not just that the markup exists.
            if not a.fresh:
                page.wait_for_selector("#stale:not([hidden])", timeout=10000)
            page.wait_for_timeout(500)
            if errors:
                sys.exit(f"the page's own script errored: {errors[:2]}")

            expect = ["backed up", "my-laptop"]
            if not a.fresh:
                expect += ["out of date"]
            checks.says(page, a.out, expect)
            checks.nothing_local_leaked(page, a.out, REPO)
            checks.no_invisible_text(page, a.out)
            # It must never carry a path or an address: this file sits on shared
            # storage that anything on the network can read.
            leaked = [s for s in ("/home/", "/media/", "//192.168", "C:\\")
                      if s in page.inner_text("body")]
            if leaked:
                sys.exit(f"{a.out}: the status page leaked {leaked}")

            page.screenshot(path=a.out, full_page=True,
                            clip=checks.clip_to_content(page, WIDTH))
            print(f"  wrote {a.out}")
            b.close()


if __name__ == "__main__":
    main()
