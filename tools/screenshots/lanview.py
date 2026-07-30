#!/usr/bin/env python3
"""Take 10-network-view.png against a REAL daemon, in a sandbox.

    python3 tools/screenshots/lanview.py

WHY THIS ONE IS NOT A FIXTURE. That page exists to show what the read-only
network view does and does not expose. Faking its contents in a fixture would let
us publish a promise the product does not actually keep — the screenshot would
show whatever we typed, not what RedactForNetwork removes. So this builds the
working tree, runs a real daemon over invented folders on invented destinations,
and asserts against the rendered page that no path, no /tmp/ and no free-space
figure appears before saving the shot.

That decision has already earned its keep: taking this shot for real is what
found the bug where an idle folder went on narrating "checking for deleted files"
for ever (fixed in v0.1.11). Six real rows sat there describing a tidy-up that had
finished. A fixture has whatever phase you put in it and could not have shown it.

SAFETY. Everything lives in a temp directory: the sandbox gets its own
XDG_CONFIG_HOME and XDG_DATA_HOME, its own source folders and its own
destinations, and free ports are chosen at runtime so a daemon you already have
running is untouched. Nothing is written inside the repo except the finished PNG.
"""
import argparse
import json
import os
import re
import shutil
import socket
import subprocess
import sys
import tempfile
import time

REPO = os.path.dirname(os.path.dirname(os.path.dirname(os.path.abspath(__file__))))
DEFAULT_OUT = os.path.join(REPO, "docs", "screenshots", "10-network-view.png")

# Invented, and deliberately coherent: a destination called "attic-nas" described
# by the product as "drive on this computer" reads as a mistake to anyone paying
# attention. Both of these are drives, so both are named like drives.
FOLDERS = ["code", "documents", "photos"]
TARGETS = ["sdcard", "usb-drive"]
MACHINE = "my-laptop"


def free_port():
    with socket.socket() as s:
        s.bind(("127.0.0.1", 0))
        return s.getsockname()[1]


def run(cmd, env, **kw):
    return subprocess.run(cmd, env=env, capture_output=True, text=True,
                          check=True, **kw)


def build_binary(sandbox):
    """Build the working tree, so the shot shows the code as it stands."""
    out = os.path.join(sandbox, "backup-maker")
    go = shutil.which("go") or os.path.expanduser(
        "~/.local/share/go-toolchain/bin/go")
    print(f"  building with {go}")
    subprocess.run([go, "build", "-o", out, "."], cwd=REPO, check=True)
    return out


def make_sandbox(sandbox, binary):
    """Set the sandbox up through the product's own CLI.

    Not by writing config.toml by hand: the drive markers and the recorded
    target UUIDs have to agree or the daemon will correctly refuse to write to
    its own destinations, and letting the CLI do it is what keeps this honest.
    """
    env = dict(os.environ,
               XDG_CONFIG_HOME=os.path.join(sandbox, "config"),
               XDG_DATA_HOME=os.path.join(sandbox, "data"))
    for name in FOLDERS:
        d = os.path.join(sandbox, "src", name)
        os.makedirs(d)
        for i in range(6):
            with open(os.path.join(d, f"file-{i}.dat"), "wb") as f:
                f.write(os.urandom(2048))
    run([binary, "init", "--name", MACHINE], env)
    for t in TARGETS:
        d = os.path.join(sandbox, "dest", t)
        os.makedirs(d)
        run([binary, "add-target", "drive", d, "--name", t], env)
    for name in FOLDERS:
        run([binary, "add-folder", os.path.join(sandbox, "src", name),
             "--label", name], env)

    cfg = os.path.join(sandbox, "config", "backup-maker", "config.toml")
    with open(cfg) as f:
        s = f.read()
    # lan_view_access = 'all' skips the per-device approval gate, which would
    # otherwise serve the holding page instead of the view we want to photograph.
    s = re.sub(r"dashboard_port = \d+",
               f"dashboard_port = {free_port()}\n"
               f"lan_view = true\n"
               f"lan_view_port = {free_port()}\n"
               f"lan_view_access = 'all'", s)
    s = s.replace("desktop_alerts = true", "desktop_alerts = false")
    with open(cfg, "w") as f:
        f.write(s)
    return env


def start_daemon(binary, env, sandbox):
    log = open(os.path.join(sandbox, "daemon.log"), "w+")
    proc = subprocess.Popen([binary, "daemon"], env=env, stdout=log, stderr=log)
    url = None
    for _ in range(60):
        time.sleep(1)
        log.flush()
        with open(log.name) as f:
            m = re.search(r'network view listening" url=(\S+)', f.read())
        if m:
            url = m.group(1)
            break
        if proc.poll() is not None:
            with open(log.name) as f:
                sys.exit("daemon exited:\n" + f.read())
    if not url:
        sys.exit("daemon never reported a network-view address")
    print(f"  network view at {url}")
    return proc, url


def wait_until_settled(binary, env, want_rows):
    """Wait for every row to be backed up AND for the scan phase to clear.

    Both halves matter. A shot taken mid-pass shows a scan nobody asked about,
    and a shot taken while a phase is stale used to show a tidy-up that had
    already finished — which is the bug this script found.
    """
    for _ in range(90):
        time.sleep(1)
        out = subprocess.run([binary, "status"], env=env, capture_output=True,
                             text=True).stdout
        rows = [l for l in out.splitlines() if " backed up " in l]
        if len(rows) >= want_rows and not any("checking" in l for l in rows):
            print(f"  {len(rows)} rows backed up and idle")
            return
    print("  WARNING: gave up waiting for the engines to settle", file=sys.stderr)


def shoot(url, out):
    from playwright.sync_api import sync_playwright

    with sync_playwright() as p:
        b = p.chromium.launch()
        page = b.new_page(viewport={"width": 1770, "height": 1200})
        page.goto(url, wait_until="domcontentloaded")
        page.wait_for_selector("#rows tr", state="attached", timeout=20000)
        page.wait_for_timeout(1200)

        text = page.inner_text("body")
        # The point of the shot: prove the redaction happened rather than trust
        # it. A leak here is a documentation screenshot publishing a real path.
        leaks = [s for s in ("/tmp/", "/home/", "free of", "GB free", REPO)
                 if s in text]
        if leaks:
            b.close()
            sys.exit(f"the network view leaked {leaks} — do not publish this shot")
        labels = page.eval_on_selector_all(
            "#rows tr td:nth-child(3) span", "n => n.map(e => e.textContent.trim())")
        print(f"  states: {sorted(set(labels))}")
        if not labels:
            b.close()
            sys.exit("nothing rendered in the status table")
        striped = page.eval_on_selector_all(
            "#rows .bar-fill",
            "n => n.filter(e => e.classList.contains('indeterminate')).length")
        if striped:
            print(f"  WARNING: {striped} rows still animating; expected none when idle",
                  file=sys.stderr)

        bottom = page.evaluate("""() => {
            let m = 0;
            for (const e of document.querySelectorAll('body *')) {
                const r = e.getBoundingClientRect();
                if (r.height && r.bottom > m) m = r.bottom;
            }
            return Math.ceil(m); }""")
        page.screenshot(path=out, full_page=True,
                        clip={"x": 0, "y": 0, "width": 1770, "height": bottom + 40})
        print(f"  wrote {out}")
        b.close()


def main():
    ap = argparse.ArgumentParser(description=__doc__,
                                 formatter_class=argparse.RawDescriptionHelpFormatter)
    ap.add_argument("--out", default=DEFAULT_OUT)
    ap.add_argument("--binary", help="skip the build and use this binary")
    ap.add_argument("--keep", action="store_true",
                    help="leave the sandbox in place for inspection")
    a = ap.parse_args()

    sandbox = tempfile.mkdtemp(prefix="bm-shots-")
    proc = None
    try:
        binary = a.binary or build_binary(sandbox)
        env = make_sandbox(sandbox, binary)
        proc, url = start_daemon(binary, env, sandbox)
        wait_until_settled(binary, env, len(FOLDERS) * len(TARGETS))
        shoot(url, a.out)
    finally:
        if proc and proc.poll() is None:
            proc.terminate()
            try:
                proc.wait(timeout=10)
            except subprocess.TimeoutExpired:
                proc.kill()
        if a.keep:
            print(f"  sandbox left at {sandbox}")
        else:
            shutil.rmtree(sandbox, ignore_errors=True)


if __name__ == "__main__":
    main()
