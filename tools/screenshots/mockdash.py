#!/usr/bin/env python3
"""A fake daemon, for taking the dashboard screenshots in the documentation.

It serves the REAL dashboard assets straight out of internal/webui/static and
answers the three endpoints the page needs — /api/ping, /api/status and
/api/events — from a canned model. So the screenshots show the dashboard exactly
as it renders, with none of the machine it was taken on in them.

WHY A FIXTURE AND NOT A REAL DAEMON. Screenshots must never carry a real machine
name, a real path or a real capacity, and a page like the tour shot needs a
household that does not exist: three folders, a drive, a share, a paired
computer, a stopped folder and 1.5TB of history. No sandbox can produce that
without a lot of pretending, and the pretending is the part that leaks.

WHAT A FIXTURE CANNOT DO, and it matters: it has whatever values you type into
it, so it can only show you what you already believed. The read-only network
view is therefore NOT shot from here — see lanview.py, which runs a real daemon
because that page's whole purpose is demonstrating what the redaction removes,
and a fixture would let us publish a promise the product does not keep. That
decision is what found the idle-phase bug fixed in v0.1.11: six real rows sat
there narrating a tidy-up that had finished.

Times are offsets recomputed on every request, not timestamps baked into the
fixture, so "9s ago" is true at the moment the shutter opens.
"""
import argparse
import json
import os
from datetime import datetime, timedelta, timezone
from http.server import SimpleHTTPRequestHandler, ThreadingHTTPServer

REPO = os.path.dirname(os.path.dirname(os.path.dirname(os.path.abspath(__file__))))
STATIC = os.path.join(REPO, "internal", "webui", "static")


def iso(seconds_ago):
    """An absolute time the given number of seconds in the past."""
    return (datetime.now(timezone.utc) - timedelta(seconds=seconds_ago)).isoformat()


def folders():
    return [
        {"id": "f-code", "label": "code", "path": "/home/alex/code",
         "continuous": True, "snapshotted": True, "protected": True},
        {"id": "f-docs", "label": "documents", "path": "/home/alex/Documents",
         "continuous": True, "snapshotted": False, "protected": True},
        {"id": "f-photos", "label": "photos", "path": "/home/alex/Pictures",
         "ignores": ["*.raw"], "continuous": True, "snapshotted": False,
         "protected": True},
    ]


def targets(sdcard_state="in sync", backups_state="in sync"):
    return [
        {"name": "sdcard", "type": "drive", "location": "/media/alex/SDCARD",
         "folder_count": 3, "all_folders": True, "state": sdcard_state,
         "last_seen": iso(9), "free_bytes": 8 * 1024**3,
         "total_bytes": 64 * 1024**3, "space_reported_at": iso(11),
         "min_free_bytes": 2 * 1024**3},
        {"name": "backups", "type": "share", "location": "//192.168.1.50/backups",
         "folder_count": 3, "all_folders": False, "state": backups_state,
         "last_seen": iso(22), "wake_enabled": True,
         "free_bytes": 640 * 1024**3, "total_bytes": 1800 * 1024**3,
         "space_reported_at": iso(24)},
        {"name": "studio-pc", "type": "device", "location": "JJJJJJJJ…",
         "folder_count": 2, "all_folders": False, "state": "in sync",
         "last_seen": iso(61)},
    ]


def row(folder, label, path, target, ttype, state="in sync", **kw):
    r = {"folder_id": folder, "folder_label": label, "folder_path": path,
         "target_name": target, "target_type": ttype, "state": state,
         "completion": 100, "need_items": 0, "need_bytes": 0,
         "last_seen": iso(kw.pop("seen", 12)), "stale": False}
    r.update(kw)
    return r


def base():
    """Everything healthy: the tour shot in the README."""
    return {
        "machine_name": "my-laptop",
        "version": "0.1.12", "commit": "v0.1.12",
        "device_id": "K7QP4M2X-9WRT6B3N-XJ5FD8HC-2VLYQ4KP",
        "engine_needed": True, "engine_ok": True, "setup_complete": True,
        "folders": folders(),
        "default_ignores": ["node_modules", "__pycache__", ".venv", "build",
                            "dist", "*.pyc", ".DS_Store", ".cache"],
        "targets": targets(),
        "retired": [{
            "id": "f-scans", "label": "scans", "path": "/home/alex/Scans",
            "stopped_at": iso(3 * 86400),
            "copies": [
                {"target": "sdcard", "type": "drive",
                 "location": "/media/alex/SDCARD",
                 "dest_path": "my-laptop/scans", "deletable": True,
                 "still_configured": True},
                {"target": "studio-pc", "type": "device", "deletable": False,
                 "still_configured": True},
            ],
        }],
        "rows": [
            row("f-code", "code", "/home/alex/code", "sdcard", "drive", seen=9),
            row("f-code", "code", "/home/alex/code", "backups", "share", seen=22),
            row("f-code", "code", "/home/alex/code", "studio-pc", "device", seen=61),
            row("f-docs", "documents", "/home/alex/Documents", "sdcard", "drive", seen=13),
            row("f-docs", "documents", "/home/alex/Documents", "backups", "share", seen=31),
            row("f-photos", "photos", "/home/alex/Pictures", "sdcard", "drive", seen=15),
        ],
        "archives": [
            {"name": "weekly-code", "folders": ["code"], "target": "backups",
             "every": "weekly", "last_run": iso(2 * 86400),
             "next_due": iso(-5 * 86400), "state": "ok", "keep": 4},
            {"name": "nightly-archive", "folders": ["code", "documents"],
             "target": "sdcard", "every": "daily", "last_run": iso(9 * 3600),
             "next_due": iso(-15 * 3600), "state": "ok", "keep": 3,
             "paused": True},
        ],
        "receive": {"enabled": True, "root": "/mnt/backups/incoming"},
        "totals": {"bytes": 1649267441664, "files": 82391,
                   "since": "2026-03-03T09:14:00Z",
                   "mirror_targets": 2, "device_targets": 1},
        "settings": {
            "desktop_alerts": True, "alert_backups_stopped": True,
            "alert_snapshot_failed": True, "alert_unrecognised_storage": True,
            "alert_pair_requests": True,
            "webhook_enabled": False, "webhook_minimal": False,
            "webhook_url_set": False,
            "ntfy_enabled": True, "ntfy_minimal": False, "ntfy_topic_set": True,
            "ntfy_token_set": False,
            "ntfy_topic_display": "https://ntfy.sh/••••••••",
            "update_check": True, "update_checked_at": iso(1800),
            "update_comparable": True,
            "lan_view": True, "lan_view_url": "http://192.168.1.24:8667",
            "lan_view_access": "approved", "lan_devices": [],
        },
    }


def scenario_healthy():
    return base()


def scenario_transferring():
    """Files moving: real byte counts, a scan with a denominator, a first backup.

    One row is deliberately "backed up" with a part-filled bar, because that is
    the design and the shot should show it: the state column answers whether the
    files are safe, the progress column what is happening right now.
    """
    m = base()
    m["totals"]["bytes"] = 214748364
    m["totals"]["files"] = 1204
    m["rows"] = [
        row("f-code", "code", "/home/alex/code", "sdcard", "drive",
            state="syncing", completion=41, need_items=612,
            need_bytes=3187671040, transferred_bytes=2306867200,
            total_bytes=5583457484, seen=1),
        row("f-code", "code", "/home/alex/code", "backups", "share",
            state="scanning", completion=0, phase="listing",
            scanned_files=18422, scan_total=72510, seen=3),
        row("f-docs", "documents", "/home/alex/Documents", "sdcard", "drive",
            state="syncing", completion=88, need_items=41,
            need_bytes=214748364, transferred_bytes=1610612736,
            total_bytes=1825361100, seen=2),
        row("f-docs", "documents", "/home/alex/Documents", "backups", "share",
            state="in sync", seen=34),
        row("f-photos", "photos", "/home/alex/Pictures", "sdcard", "drive",
            state="scanning", completion=0, phase="source",
            first_backup=True, seen=1),
    ]
    return m


def scenario_offline():
    """Things wrong, and one thing merely waiting on a person.

    Four different kinds at once, because they are drawn differently on purpose:
    a share that has gone away, a paired computer stale for long enough to earn a
    warning triangle, a snapshot that failed, and a machine asking to back up
    here — which is a decision, not a fault.
    """
    m = base()
    m["targets"] = targets(backups_state="offline")
    m["targets"][1]["last_seen"] = iso(4 * 3600)
    m["targets"][1]["space_reported_at"] = iso(4 * 3600)
    m["targets"][2]["state"] = "stale"
    m["targets"][2]["last_seen"] = iso(9 * 86400)
    m["archives"][0]["state"] = "failed"
    m["archives"][0]["detail"] = "destination was not reachable"
    m["pending_sources"] = [{"device_id": "M4XR7QP2-K9WN6B3T-YJ5FD8HC-2VLZQ4KR",
                             "name": "attic-pi", "address": "192.168.1.31:22000"}]
    for r in m["rows"]:
        if r["target_name"] == "backups":
            r.update(state="offline", stale=True, completion=100,
                     last_seen=iso(4 * 3600),
                     detail="the destination has not answered for 4 hours")
        if r["target_name"] == "studio-pc":
            r.update(state="stale", stale=True, last_seen=iso(9 * 86400))
    return m


SCENARIOS = {
    "healthy": scenario_healthy,
    "transferring": scenario_transferring,
    "offline": scenario_offline,
}


class Handler(SimpleHTTPRequestHandler):
    scenario = "healthy"

    def __init__(self, *a, **kw):
        super().__init__(*a, directory=STATIC, **kw)

    def log_message(self, *a):
        pass  # the shot is the output; a request log is noise

    def _json(self, obj):
        body = json.dumps(obj).encode()
        self.send_response(200)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def do_GET(self):
        p = self.path.split("?")[0]
        if p == "/api/ping":
            return self._json({"ping": "pong"})
        if p == "/api/status":
            return self._json(SCENARIOS[self.scenario]())
        if p == "/api/events":
            # A valid but silent stream. A 404 here sends the dashboard into a
            # reconnect loop that fills the console with errors.
            self.send_response(200)
            self.send_header("Content-Type", "text/event-stream")
            self.end_headers()
            try:
                self.wfile.write(b": open\n\n")
                self.wfile.flush()
            except Exception:
                pass
            return
        return super().do_GET()


def serve(scenario, port=0):
    """Start a server in a background thread. Returns (httpd, port)."""
    import threading

    handler = type("H", (Handler,), {"scenario": scenario})
    httpd = ThreadingHTTPServer(("127.0.0.1", port), handler)
    threading.Thread(target=httpd.serve_forever, daemon=True).start()
    return httpd, httpd.server_address[1]


if __name__ == "__main__":
    ap = argparse.ArgumentParser(description=__doc__)
    ap.add_argument("--port", type=int, default=8899)
    ap.add_argument("--scenario", default="healthy", choices=sorted(SCENARIOS))
    a = ap.parse_args()
    print(f"serving {a.scenario} on http://127.0.0.1:{a.port}  (assets: {STATIC})")
    Handler.scenario = a.scenario
    ThreadingHTTPServer(("127.0.0.1", a.port), Handler).serve_forever()
