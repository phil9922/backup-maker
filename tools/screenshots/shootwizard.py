#!/usr/bin/env python3
"""Take the setup-wizard and adopt screenshots in docs/screenshots.

    python3 tools/screenshots/shootwizard.py
    python3 tools/screenshots/shootwizard.py --out /tmp/shots --only 03-wizard-destinations

Each shot is produced by DRIVING the wizard to the step in question — clicking
through kind, folder and destinations the way somebody setting up a backup would
— against the fixture in wizardmock.py. Nothing is ever submitted: the mock has no
POST /api/backups and no POST /api/adopt on purpose, so a script that accidentally
pressed the final button gets a 404 rather than a screenshot of a lie.

Read the notes at the top of shoot.py first — the same three traps apply
(`networkidle` never fires, phone rows are not "visible", full-page shots are
enormous).
"""
import argparse
import os
import sys

import checks
import wizardmock
from playwright.sync_api import sync_playwright

REPO = wizardmock.mockdash.REPO
DEFAULT_OUT = os.path.join(REPO, "docs", "screenshots")
WIDE, PHONE = 1770, 390


def settle(page, ms=900):
    page.wait_for_timeout(ms)


def open_wizard(page, port):
    page.goto(f"http://127.0.0.1:{port}/", wait_until="domcontentloaded")
    # A machine with nothing configured shows the wizard on arrival. One that
    # already protects something shows the dashboard, and the wizard is behind
    # "Set up a backup" — which is the only way to reach the shot of the folder
    # step offering a folder that is already protected.
    page.wait_for_function(
        "() => { const t = document.getElementById('wiz-title');"
        " const b = document.getElementById('open-wizard');"
        " return (t && t.textContent.trim() && t.offsetParent) || (b && b.offsetParent); }",
        timeout=15000)
    if page.evaluate("() => { const b = document.getElementById('open-wizard');"
                     " return !!(b && b.offsetParent); }"):
        page.click("#open-wizard")
        page.wait_for_function(
            "() => { const e = document.getElementById('wiz-title');"
            " return e && e.textContent.trim().length > 0; }", timeout=15000)
    settle(page, 600)


def choose_kind(page, value):
    page.check(f'input[name="mode"][value="{value}"]')
    settle(page, 300)


def to_folder_step(page, value="incremental"):
    choose_kind(page, value)
    page.click("#wiz-next")
    settle(page)


def into_desktop(page):
    """Open /home/alex/Desktop, which is where the folder picker shot is taken.

    Inside a directory rather than at the roots list, because that is where the
    picker has something to show: a path bar, the "Protect this folder" action for
    the directory you are standing in, and entries with their own buttons.
    """
    page.click("#wizard button:text-is('Desktop')")
    settle(page)


def to_dest_step(page):
    page.click("#pick-here")
    settle(page, 400)
    page.click("#wiz-next")
    settle(page, 1100)


def fill_destinations(page):
    """Scan, expand this computer and the NAS, and tick three of the four."""
    page.click("#dest-scan")
    settle(page, 1100)
    page.click("#wizard button:has-text('my-laptop')")
    settle(page)
    page.click("#wizard button:has-text('NAS')")
    settle(page)
    boxes = page.query_selector_all("#wizard input[type=checkbox]")
    # SDCARD, BackupSSD and //…/backups — one share deliberately left unticked,
    # so the shot shows a choice rather than everything selected.
    for i in (0, 1, 2):
        if i < len(boxes):
            boxes[i].check()
    settle(page, 400)


def save(page, out, width, expect):
    """Screenshot, once the page has passed every check in checks.py."""
    checks.says(page, out, expect)
    checks.nothing_local_leaked(page, out, REPO)
    checks.no_invisible_text(page, out)
    page.screenshot(path=out, full_page=True,
                    clip=checks.clip_to_content(page, width))
    print(f"    wrote {os.path.basename(out)}")


# Each entry: (configured?, width, driver, phrases that must be on the page)
def shot_kind(page):
    pass  # step 1 is where the wizard opens


def shot_folder(page):
    to_folder_step(page)
    into_desktop(page)


def shot_destinations(page):
    to_folder_step(page)
    into_desktop(page)
    to_dest_step(page)
    fill_destinations(page)


def shot_review(page):
    shot_destinations(page)
    page.click("#wiz-next")
    settle(page, 900)


def shot_schedule(page):
    to_folder_step(page, "timed")
    into_desktop(page)
    to_dest_step(page)
    fill_destinations(page)
    page.click("#wiz-next")
    settle(page, 900)


def shot_existing_folder(page):
    to_folder_step(page)


def shot_restore_choice(page):
    choose_kind(page, "adopt")


def shot_adopt_identity(page):
    choose_kind(page, "adopt")
    page.click("#wiz-next")
    settle(page, 1400)
    # The adopt flow opens on "Where are your backups?" — the found drive is
    # chosen with its own button, not by clicking the machine's name, and doing so
    # ADVANCES to the identity step by itself. Pressing Continue as well overshoots
    # to Folders, which is why this does not.
    page.click("#adopt button:text-is('Restore from this')")
    settle(page, 1400)


SHOTS = {
    "01-wizard-kind": (False, WIDE, shot_kind,
                       ["What kind of backup is this?", "Incremental", "Timed",
                        "Restore this machine"]),
    "02-wizard-folder": (False, WIDE, shot_folder,
                         ["Which folders should be protected?",
                          "/home/alex/Desktop", "Protect this folder", "Development"]),
    "03-wizard-destinations": (False, WIDE, shot_destinations,
                               ["Where should the copies go?", "SDCARD",
                                "/media/alex/SDCARD", "//192.168.1.50/backups",
                                "other computer(s) sharing storage"]),
    "04-wizard-review": (False, WIDE, shot_review,
                         ["Ready to start", "Nothing has been saved yet"]),
    "05-wizard-schedule": (False, WIDE, shot_schedule,
                           ["How often, and what password?"]),
    "06-wizard-existing-folder": (True, WIDE, shot_existing_folder,
                                  ["Which folders should be protected?", "code"]),
    "12-wizard-restore-choice": (False, WIDE, shot_restore_choice,
                                 ["Restore this machine", "I already have backups"]),
    "13-adopt-identity": (False, WIDE, shot_adopt_identity,
                          ["Is this the same machine?", "old-laptop"]),
    "mobile-wizard": (False, PHONE, shot_destinations,
                      ["Where should the copies go?", "SDCARD"]),
}


def main():
    ap = argparse.ArgumentParser(description=__doc__,
                                 formatter_class=argparse.RawDescriptionHelpFormatter)
    ap.add_argument("--out", default=DEFAULT_OUT)
    ap.add_argument("--only", action="append", choices=sorted(SHOTS))
    ap.add_argument("--keep-going", action="store_true")
    a = ap.parse_args()
    os.makedirs(a.out, exist_ok=True)
    wanted = a.only or sorted(SHOTS)

    ports = {False: wizardmock.serve(False)[1], True: wizardmock.serve(True)[1]}
    failed = []
    with sync_playwright() as p:
        browser = p.chromium.launch()
        for name in wanted:
            configured, width, drive, expect = SHOTS[name]
            print(f"  {name} ({width}px{', already configured' if configured else ''})")
            page = browser.new_page(viewport={"width": width, "height": 1100},
                                    device_scale_factor=2 if width == PHONE else 1)
            errors = []
            page.on("pageerror", lambda e: errors.append(str(e)))
            try:
                open_wizard(page, ports[configured])
                drive(page)
                if errors:
                    raise SystemExit(f"javascript errors on the way: {errors[:2]}")
                save(page, os.path.join(a.out, name + ".png"), width, expect)
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


if __name__ == "__main__":
    main()
