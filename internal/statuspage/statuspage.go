// SPDX-License-Identifier: MIT

// Package statuspage renders a self-contained status page that backup-maker
// writes onto each destination.
//
// Why it exists: the dashboard is served by the computer being backed up, so it
// goes dark exactly when you most want it — when that machine is off, broken or
// stolen. A destination that is always on (a Pi, a NAS) can hold a page that
// still answers, because it is just a file sitting next to the backups.
//
// THE RULE THIS PAGE LIVES BY: a status page that cheerfully reports "all in
// sync" from a machine that died last week is worse than no page at all. It is
// false reassurance, and a backup tool must never give that. So the page leads
// with how long ago it was written, and past a threshold it stops reporting
// health and says plainly that backups are probably not running.
package statuspage

import (
	"bytes"
	"fmt"
	"html/template"
	"path"
	"time"

	"github.com/phil9922/backup-maker/internal/config"
)

// FileName is what one machine's page is called. It lands INSIDE that machine's
// directory (see PathFor), because a destination can be shared by several
// computers and at the root there is only room for one of them — so the last
// machine to write simply erased the others, and a page reporting one machine's
// health while silently omitting another's is the false reassurance the rule
// below exists to prevent.
//
// The same name is reused for the index at the destination root (see
// RenderIndex): every bookmark, symlink and web-server config that already
// points at backup-maker-status.html keeps working, and now lands on something
// that lists every machine rather than whichever wrote last.
const FileName = "backup-maker-status.html"

// PathFor is where one machine's page lives on a destination.
func PathFor(machineName string) string { return path.Join(dirFor(machineName), FileName) }

// dirFor names a machine's directory. It goes through config.MachineDir so the
// page lands in the same directory the mirror writes to, on every filesystem
// that needs a character replaced.
func dirFor(machineName string) string { return config.MachineDir(machineName) }

// StaleAfter is when the page stops trusting itself. The daemon rewrites every
// minute or so; an hour of silence means the source machine is off, asleep, or
// gone.
const StaleAfter = time.Hour

// Row is one folder × destination line, already redacted.
type Row struct {
	Folder      string
	Destination string
	// State is the WORD SHOWN, not the engine's internal name — translated by
	// status.RowLabel or status.ArchiveLabel before it gets here, so this page
	// says "backed up" like every other surface rather than "in sync".
	State string
	// Health is the colour that goes with it: "ok", "busy", "bad" or "muted",
	// from status.RowHealth or status.ArchiveHealth.
	//
	// Decided in Go rather than by comparing strings in the template, which is
	// how this page came to paint a scanning mirror and a merely-due snapshot
	// red. A template that branches on state is a second copy of the mapping.
	Health string
	Detail string
}

// StorageLine is one destination's capacity, pre-formatted so the template
// stays logic-free. Capacity is health-adjacent ("is this filling up?"), not a
// path or address, so it belongs on a page that otherwise carries no map of the
// machine.
type StorageLine struct {
	Destination string
	Free        string
	Total       string
	UsedPct     int
	// Unavailable marks a destination that could not report its capacity at
	// all. It gets a line saying so rather than being left out: silence there
	// is indistinguishable from health, and the reserve that keeps space free
	// is not being enforced on a destination nobody can measure.
	Unavailable bool
}

// Page is everything the rendered file needs.
//
// Note what is absent: folder paths, destination addresses, device IDs. The
// page sits on shared storage that anything on the network can read, so it
// carries health, not a map of the machine.
type Page struct {
	Machine   string
	Written   time.Time
	Rows      []Row
	Snapshots []Row
	Storage   []StorageLine
}

// Render produces the complete page: one file, no external assets, so it works
// opened straight off a file share with no web server at all.
func Render(p Page) ([]byte, error) {
	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, struct {
		Page
		WrittenISO  string
		WrittenText string
		StaleSecs   int
	}{
		Page:        p,
		WrittenISO:  p.Written.Format(time.RFC3339),
		WrittenText: p.Written.Format("2 Jan 2006, 15:04 MST"),
		StaleSecs:   int(StaleAfter / time.Second),
	}); err != nil {
		return nil, fmt.Errorf("rendering status page: %w", err)
	}
	return buf.Bytes(), nil
}

// The staleness banner is computed in the viewer's browser rather than baked in
// at write time, because the file may be read days after it was written. The
// page must be able to say "this is old" without anything running.
var tmpl = template.Must(template.New("status").Parse(`<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>{{.Machine}} — backup status</title>
<style>
 :root { color-scheme: dark; font-family: system-ui, sans-serif;
   --bg:#10141a; --fg:#e6e9ee; --muted:#8a94a3; --ok:#66bb6a; --busy:#ffca28; --bad:#ef5350; }
 body { background:var(--bg); color:var(--fg); margin:0; padding:1.5rem; }
 .wrap { max-width:52rem; margin:0 auto; }
 h1 { font-size:1.2rem; margin:0 0 .25rem; }
 .muted { color:var(--muted); }
 .age { font-size:1rem; margin:.25rem 0 1.25rem; }
 .stale { background:#2b1a1a; border:1px solid #6d2f2f; border-radius:8px;
   padding:1rem 1.25rem; margin:0 0 1.5rem; line-height:1.6; }
 .stale strong { display:block; font-size:1.05rem; margin-bottom:.35rem; }
 table { border-collapse:collapse; width:100%; margin-bottom:1.5rem; }
 th,td { text-align:left; padding:.45rem .7rem; white-space:nowrap; }
 th { color:var(--muted); font-weight:500; font-size:.85rem; border-bottom:1px solid #232a34; }
 tr+tr td { border-top:1px solid #1a212b; }
 .ok{color:var(--ok)} .busy{color:var(--busy)} .bad{color:var(--bad)}
 footer { color:var(--muted); font-size:.85rem; border-top:1px solid #232a34;
   padding-top:1rem; line-height:1.6; }
</style>
</head>
<body><div class="wrap">
<h1>{{.Machine}} — backup status</h1>
<p class="age muted" id="age">written {{.WrittenText}}</p>

<div class="stale" id="stale" hidden>
  <strong>This page is out of date — treat it as history, not status.</strong>
  <span id="stale-detail"></span>
  It is written by {{.Machine}} while that computer is running. If it has
  stopped updating, that machine is off, asleep, or no longer backing up —
  the backups themselves are still on this drive, but nothing new is arriving.
</div>

<div id="live">
{{if .Rows}}
<table>
<thead><tr><th>Folder</th><th>Destination</th><th>State</th><th></th></tr></thead>
<tbody>
{{range .Rows}}<tr>
 <td>{{.Folder}}</td><td>{{.Destination}}</td>
 <td class="{{.Health}}">{{.State}}</td>
 <td class="muted">{{.Detail}}</td>
</tr>{{end}}
</tbody></table>
{{else}}<p class="muted">No folders are being backed up.</p>{{end}}

{{if .Storage}}
<h1>Storage</h1>
<table>
<thead><tr><th>Destination</th><th>Free</th><th>Capacity</th><th>Used</th></tr></thead>
<tbody>
{{range .Storage}}<tr>
 <td>{{.Destination}}</td>
{{if .Unavailable}} <td class="bad" colspan="3">free space unavailable — reserve not enforced</td>
{{else}} <td>{{.Free}}</td><td>{{.Total}}</td><td class="muted">{{.UsedPct}}%</td>{{end}}
</tr>{{end}}
</tbody></table>
{{end}}

{{if .Snapshots}}
<h1>Timed snapshots</h1>
<table>
<thead><tr><th>Name</th><th>Destination</th><th>State</th><th>Last run</th></tr></thead>
<tbody>
{{range .Snapshots}}<tr>
 <td>{{.Folder}}</td><td>{{.Destination}}</td>
 <td class="{{.Health}}">{{.State}}</td>
 <td class="muted">{{.Detail}}</td>
</tr>{{end}}
</tbody></table>
{{end}}
</div>

<footer>
Written by backup-maker on {{.Machine}}. This is a snapshot of what that
computer reported, saved next to your backups so it can be read even when the
computer is off. Setting up or changing backups is only possible there.
Paths and addresses are deliberately omitted.
</footer>
</div>
<script>
(function () {
  var written = new Date({{.WrittenISO}});
  var staleSecs = {{.StaleSecs}};
  // Returns a bare duration ("3 days") so it reads correctly both as
  // "last reported 3 days ago" and "nothing reported for 3 days".
  function human(s) {
    if (s < 60) return s + " seconds";
    if (s < 3600) return Math.floor(s / 60) + " minutes";
    if (s < 172800) return Math.floor(s / 3600) + " hours";
    return Math.floor(s / 86400) + " days";
  }
  function tick() {
    var age = Math.max(0, Math.floor((Date.now() - written.getTime()) / 1000));
    document.getElementById("age").textContent = "last reported " + human(age) + " ago";
    if (age > staleSecs) {
      // Past the threshold the page stops presenting itself as current. A
      // stale "in sync" is the one lie a backup tool must never tell.
      document.getElementById("stale").hidden = false;
      document.getElementById("stale-detail").textContent =
        "Nothing has been reported for " + human(age) + ". ";
      document.getElementById("live").style.opacity = "0.55";
    }
  }
  tick();
  setInterval(tick, 1000);
})();
</script>
</body></html>
`))
