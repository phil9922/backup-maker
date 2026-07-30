#!/usr/bin/env python3
"""Take the dashboard screenshots in docs/screenshots.

    python3 tools/screenshots/shoot.py            # write into docs/screenshots
    python3 tools/screenshots/shoot.py --out /tmp/shots --keep-going

Starts the fixture from mockdash.py in-process, drives a headless Chromium over
each scenario, and reports what actually rendered before saving — so a shot can
never be quietly empty or quietly grey.

THREE THINGS THAT WILL WASTE AN HOUR IF YOU DO NOT KNOW THEM:

  * `networkidle` NEVER FIRES. The dashboard holds an SSE connection open, so
    the network is never idle. Wait for `domcontentloaded` and then for a
    selector that only exists once the status has been rendered.

  * ON A PHONE THE TABLE ROWS ARE NOT VISIBLE. The narrow layout stacks each row
    into a card and hides the header row, so Playwright's default visibility wait
    times out on a page that rendered perfectly. Use state="attached".

  * A FULL-PAGE PHONE SHOT IS ABOUT 10,000px TALL, which is useless in a
    document. Shots are clipped: the desktop ones to just below the running-total
    line (measured, not hard-coded, so a scenario with an extra row does not get
    its last line sliced off) and the phone one to a fixed height.
"""
import argparse
import os
import sys

import checks
import mockdash
from playwright.sync_api import sync_playwright

REPO = mockdash.REPO
DEFAULT_OUT = os.path.join(REPO, "docs", "screenshots")

# name -> (scenario, viewport width, mobile?, clip)
# clip: "totals" = measured, just below the totals line; an int = CSS pixels.
SHOTS = {
    "07-dashboard": ("healthy", 1770, False, "totals"),
    "08-transferring": ("transferring", 1770, False, "totals"),
    "09-offline": ("offline", 1770, False, "totals"),
    "mobile-dashboard": ("healthy", 390, True, 1638),
}


def shoot(page, out, width, clip):
    page.wait_for_selector("#rows tr", timeout=15000, state="attached")
    page.wait_for_timeout(1200)  # let the bars finish their transition

    rows = page.eval_on_selector_all("#rows tr", "n => n.length")
    # Each state's class and the colour it ACTUALLY computes to in the browser.
    states = page.eval_on_selector_all("#rows tr td:nth-child(3) span", """
        n => n.map(e => ({
            text: e.textContent.trim(),
            cls: e.className.split(' ')[0],
            colour: getComputedStyle(e).color,
        }))""")
    # What each class is supposed to compute to, read from the stylesheet's own
    # variables rather than written down here — hard-coding a colour makes the
    # check a second copy of the palette, and a wrong guess makes it useless.
    want = page.evaluate("""() => {
        const s = getComputedStyle(document.documentElement);
        const rgb = name => {
            const h = s.getPropertyValue(name).trim().replace('#', '');
            const v = h.length === 3 ? h.split('').map(c => c + c).join('') : h;
            return 'rgb(' + [0, 2, 4].map(i => parseInt(v.substr(i, 2), 16)).join(', ') + ')';
        };
        return {ok: rgb('--ok'), busy: rgb('--busy'), bad: rgb('--bad')};
    }""")

    print(f"    rows={rows}")
    for s in states:
        print(f"      {s['text']} [{s['cls']}] {s['colour']}")
    if not states:
        raise SystemExit(f"{out}: nothing rendered in the status table")

    # THE CHECK THIS HARNESS EXISTS FOR. v0.1.10 shipped a dashboard where every
    # state was drawn in the body colour: the words were all correct, nothing
    # errored, and a broken destination looked exactly like a healthy one. It went
    # unnoticed for two releases. So a shot is refused unless each state's colour
    # is the one its class promises.
    for s in states:
        expected = want.get(s["cls"])
        if expected and s["colour"] != expected:
            raise SystemExit(
                f"{out}: the {s['cls']!r} state ({s['text']!r}) computes to "
                f"{s['colour']} but --{s['cls']} is {expected}. The stylesheet is "
                "not reaching the element the class is on — refusing to publish a "
                "shot in which a fault and a healthy destination look alike.")

    checks.no_invisible_text(page, out)
    checks.nothing_local_leaked(page, out, REPO)

    height = clip
    if clip == "totals":
        box = page.locator("#totals").first.bounding_box()
        height = int(box["y"] + box["height"] + 46)
    page.screenshot(path=out, full_page=True,
                    clip={"x": 0, "y": 0, "width": width, "height": height})
    print(f"    wrote {out} ({width}x{height} CSS px)")


def main():
    ap = argparse.ArgumentParser(description=__doc__,
                                 formatter_class=argparse.RawDescriptionHelpFormatter)
    ap.add_argument("--out", default=DEFAULT_OUT,
                    help="directory to write into (default: docs/screenshots)")
    ap.add_argument("--only", action="append", choices=sorted(SHOTS),
                    help="just this shot; repeatable")
    ap.add_argument("--keep-going", action="store_true",
                    help="do not stop at the first failure")
    a = ap.parse_args()
    os.makedirs(a.out, exist_ok=True)
    wanted = a.only or sorted(SHOTS)

    servers = {}
    for name in wanted:
        scenario = SHOTS[name][0]
        if scenario not in servers:
            servers[scenario] = mockdash.serve(scenario)[1]

    failed = []
    with sync_playwright() as p:
        browser = p.chromium.launch()
        for name in wanted:
            scenario, width, mobile, clip = SHOTS[name]
            print(f"  {name} ({scenario}, {width}px{', phone' if mobile else ''})")
            page = browser.new_page(viewport={"width": width, "height": 1200},
                                    device_scale_factor=2 if mobile else 1)
            try:
                page.goto(f"http://127.0.0.1:{servers[scenario]}/",
                          wait_until="domcontentloaded")
                shoot(page, os.path.join(a.out, name + ".png"), width, clip)
            except Exception as e:
                failed.append(f"{name}: {e}")
                print(f"    FAILED: {e}", file=sys.stderr)
                if not a.keep_going:
                    browser.close()
                    raise SystemExit(1)
            finally:
                page.close()
        browser.close()

    if failed:
        print("\nfailed:", *failed, sep="\n  ", file=sys.stderr)
        raise SystemExit(1)
    print("\n10-network-view.png is NOT taken here — it is shot against a real "
          "daemon.\nRun: python3 tools/screenshots/lanview.py")


if __name__ == "__main__":
    main()
