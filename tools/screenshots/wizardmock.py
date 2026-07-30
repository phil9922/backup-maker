#!/usr/bin/env python3
"""A fake daemon for the setup-wizard and adopt screenshots.

Same idea and same rule as mockdash.py — invent the household, never show the
machine this runs on — but the wizard needs more than a status model: it browses
folders, lists computers, and asks each one what storage it offers.

WHY THESE ARE MOCKED TOO. The published wizard screenshots show a NAS offering
two shares, a computer that needs a password, and a router. No sandbox discovers
that. Driving a real wizard would produce a truthful but useless picture — one
folder, one drive, no network — and the documentation is explaining the shape of
the decision, not this machine's hardware.

It also means no filesystem trickery is needed. The paths in these shots come
from this file, not from a directory anybody has to create.
"""
import argparse
import json
import os
from http.server import SimpleHTTPRequestHandler, ThreadingHTTPServer

import mockdash

STATIC = mockdash.STATIC
HOME = "/home/alex"

# The folder tree the picker walks. Keys are paths; values are subdirectory names.
TREE = {
    HOME: ["Desktop", "Documents", "Pictures", "code", "notes"],
    f"{HOME}/Desktop": ["Development", "notes"],
    f"{HOME}/Documents": ["invoices", "letters"],
    f"{HOME}/Pictures": ["2026", "raw"],
    f"{HOME}/code": ["backup-maker", "scratch"],
    f"{HOME}/notes": [],
}

ROOTS = [
    {"name": "Home", "path": HOME},
    {"name": "Documents", "path": f"{HOME}/Documents"},
    {"name": "Desktop", "path": f"{HOME}/Desktop"},
    {"name": "Pictures", "path": f"{HOME}/Pictures"},
]

MACHINES = [
    {"id": "this", "name": "my-laptop", "kind": "this", "browsable": True,
     "note": "drives plugged into or inside this computer"},
    {"id": "192.168.1.50", "name": "NAS", "kind": "smb", "addr": "192.168.1.50",
     "browsable": True},
    {"id": "192.168.1.23", "name": "STUDIO-PC", "kind": "smb",
     "addr": "192.168.1.23", "needs_auth": True, "browsable": True},
    {"id": "192.168.1.1", "name": "ROUTER", "kind": "smb", "addr": "192.168.1.1",
     "browsable": True},
]

STORAGE = {
    "this": [
        {"kind": "drive", "label": "SDCARD", "path": "/media/alex/SDCARD",
         "free": 52 * 1024**3, "total": 64 * 1024**3},
        {"kind": "drive", "label": "BackupSSD", "path": "/media/alex/BackupSSD",
         "free": 806 * 1024**3, "total": 1024 * 1024**3},
    ],
    "192.168.1.50": [
        {"kind": "share", "label": "backups", "url": "//192.168.1.50/backups"},
        {"kind": "share", "label": "media", "url": "//192.168.1.50/media"},
    ],
}


# The adopt flow: a drive found with a manifest on it, and what adopting it would
# restore. "old-laptop" rather than this machine's name, because the whole point
# of the flow is rebuilding a computer that is gone.
ADOPT_CANDIDATES = [{
    "path": "/media/alex/SDCARD", "machine_name": "old-laptop",
    "folders": 3, "targets": 2, "archives": 1,
    "generated": "2026-07-27T22:15:00Z",
}]

ADOPT_INSPECTION = {
    "machine_name": "old-laptop",
    "generated": "2026-07-27T22:15:00Z",
    "folders": [
        {"id": "f-code", "path": f"{HOME}/code", "label": "code", "exists": True},
        {"id": "f-docs", "path": f"{HOME}/Documents", "label": "documents", "exists": True},
        {"id": "f-pics", "path": "/home/old/Pictures", "label": "photos", "exists": False},
    ],
    "targets": [
        {"name": "SDCARD", "type": "drive", "location": "/media/alex/SDCARD",
         "pointed_at": True, "has_uuid": True},
        {"name": "backups", "type": "share", "location": "//192.168.1.50/backups",
         "username": "alex", "pointed_at": False, "has_uuid": False},
    ],
    "archives": [{"name": "weekly-code", "every": "weekly", "target": "backups"}],
}


def status(configured=False):
    """The model the wizard reads.

    setup_complete=False is what puts the dashboard into first-run mode, which is
    the state every wizard screenshot is taken in. The configured variant is for
    the shot where the wizard is opened again on a machine that already protects
    something, and offers that folder for reuse.
    """
    m = {
        "machine_name": "my-laptop",
        "version": "0.1.12", "commit": "v0.1.12",
        "device_id": "K7QP4M2X-9WRT6B3N-XJ5FD8HC-2VLYQ4KP",
        "engine_needed": False, "engine_ok": True,
        "setup_complete": configured,
        "folders": [], "targets": [], "rows": [], "archives": [],
        "receive": {"enabled": False},
        "totals": {"bytes": 0, "files": 0, "mirror_targets": 0, "device_targets": 0},
        "settings": {
            "desktop_alerts": True, "update_check": True, "update_comparable": True,
            "lan_view": False, "lan_devices": [],
        },
    }
    if configured:
        m["folders"] = [{"id": "f-code", "label": "code", "path": f"{HOME}/code",
                         "continuous": True, "snapshotted": False, "protected": True}]
        m["targets"] = [{"name": "SDCARD", "type": "drive",
                         "location": "/media/alex/SDCARD", "folder_count": 1,
                         "all_folders": True, "state": "in sync",
                         "free_bytes": 52 * 1024**3, "total_bytes": 64 * 1024**3}]
        m["rows"] = [{"folder_id": "f-code", "folder_label": "code",
                      "folder_path": f"{HOME}/code", "target_name": "SDCARD",
                      "target_type": "drive", "state": "in sync", "completion": 100,
                      "need_items": 0, "need_bytes": 0, "stale": False}]
        m["totals"] = {"bytes": 4294967296, "files": 1841, "mirror_targets": 1,
                       "device_targets": 0}
    return m


def listing(path):
    kids = TREE.get(path)
    if kids is None:
        return None
    parent = os.path.dirname(path) if path != "/" else ""
    return {"path": path, "parent": parent,
            "entries": [{"name": k, "path": f"{path}/{k}"} for k in kids]}


class Handler(SimpleHTTPRequestHandler):
    configured = False

    def __init__(self, *a, **kw):
        super().__init__(*a, directory=STATIC, **kw)

    def log_message(self, *a):
        pass

    def _json(self, obj, code=200):
        body = json.dumps(obj).encode()
        self.send_response(code)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def do_GET(self):
        from urllib.parse import urlparse, parse_qs, unquote

        u = urlparse(self.path)
        q = parse_qs(u.query)
        if u.path == "/api/ping":
            return self._json({"ping": "pong"})
        if u.path == "/api/status":
            return self._json(status(self.configured))
        if u.path == "/api/browse":
            path = unquote(q.get("path", [""])[0])
            if not path:
                return self._json({"roots": ROOTS})
            got = listing(path)
            if got is None:
                return self._json({"error": "no such folder"}, 422)
            return self._json(got)
        if u.path == "/api/machines":
            return self._json(MACHINES)
        if u.path == "/api/adopt/scan":
            return self._json({"candidates": ADOPT_CANDIDATES})
        if u.path == "/api/drives/unusable":
            return self._json({"drives": [], "can_prepare": False,
                               "command_prefix": "", "allow_command": ""})
        if u.path == "/api/events":
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

    def do_POST(self):
        length = int(self.headers.get("Content-Length") or 0)
        body = json.loads(self.rfile.read(length) or b"{}")
        if self.path == "/api/machines/storage":
            return self._json(STORAGE.get(body.get("machine"), []))
        if self.path == "/api/adopt/inspect":
            return self._json(ADOPT_INSPECTION)
        if self.path == "/api/adopt/test-share":
            return self._json({"ok": True})
        # Nothing here commits anything: these shots are taken on the way through,
        # and a mock that accepted a submission would be pretending to have done
        # something. /api/backups and /api/adopt are deliberately absent.
        return self._json({"error": "not mocked (by design): " + self.path}, 404)


def serve(configured=False, port=0):
    import threading

    handler = type("H", (Handler,), {"configured": configured})
    httpd = ThreadingHTTPServer(("127.0.0.1", port), handler)
    threading.Thread(target=httpd.serve_forever, daemon=True).start()
    return httpd, httpd.server_address[1]


if __name__ == "__main__":
    ap = argparse.ArgumentParser(description=__doc__)
    ap.add_argument("--port", type=int, default=8898)
    ap.add_argument("--configured", action="store_true",
                    help="a machine that already protects a folder")
    a = ap.parse_args()
    Handler.configured = a.configured
    print(f"serving the wizard on http://127.0.0.1:{a.port}")
    ThreadingHTTPServer(("127.0.0.1", a.port), Handler).serve_forever()
