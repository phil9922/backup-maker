// SPDX-License-Identifier: MIT

package statuspage

import (
	"bytes"
	"fmt"
	"html/template"
	"time"
)

// IndexEntry is one machine's page as it was found ON THE DRIVE — not as the
// machine writing the index believes the world to be.
//
// THAT DISTINCTION IS THE WHOLE DESIGN. Every computer using a destination
// writes this same root file, so if each described what it knew, the index
// would flip between versions minute by minute and each machine would report
// the others as missing. Derived from the directory listing instead, two
// machines produce the same file, and a machine that has stopped writing
// altogether still appears — marked stale, which is the fact somebody opening
// this page most needs.
type IndexEntry struct {
	Machine string
	Written time.Time
	// Stale marks a page nothing has refreshed lately. Computed at write time
	// from the file's timestamp, unlike the per-machine page's own banner,
	// which is computed in the viewer's browser: this file may be rewritten
	// every minute by a machine that is fine, so its own age says nothing about
	// the machine whose row it is.
	Stale bool
}

// RenderIndex produces the destination-root page listing every machine that has
// backups here. Self-contained, like the per-machine page, so it works opened
// straight off a drive with no web server at all.
func RenderIndex(entries []IndexEntry, now time.Time) ([]byte, error) {
	type row struct {
		IndexEntry
		Href string
		Age  string
	}
	rows := make([]row, len(entries))
	for i, e := range entries {
		age := "never"
		if !e.Written.IsZero() {
			age = humanAge(now.Sub(e.Written))
		}
		rows[i] = row{IndexEntry: e, Href: dirFor(e.Machine) + "/" + FileName, Age: age}
	}
	var buf bytes.Buffer
	if err := indexTmpl.Execute(&buf, struct {
		Rows        []row
		WrittenText string
	}{Rows: rows, WrittenText: now.Format("2 Jan 2006, 15:04 MST")}); err != nil {
		return nil, fmt.Errorf("rendering status index: %w", err)
	}
	return buf.Bytes(), nil
}

func humanAge(d time.Duration) string {
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%d minutes ago", int(d/time.Minute))
	case d < 48*time.Hour:
		return fmt.Sprintf("%d hours ago", int(d/time.Hour))
	default:
		return fmt.Sprintf("%d days ago", int(d/(24*time.Hour)))
	}
}

var indexTmpl = template.Must(template.New("index").Parse(`<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>backup status</title>
<style>
 :root { color-scheme: dark; font-family: system-ui, sans-serif;
   --bg:#10141a; --fg:#e6e9ee; --muted:#8a94a3; --ok:#66bb6a; --bad:#ef5350; }
 body { background:var(--bg); color:var(--fg); margin:0; padding:1.5rem; line-height:1.5; }
 .wrap { max-width:52rem; margin:0 auto; }
 h1 { font-size:1.2rem; margin:0 0 1.25rem; }
 .muted { color:var(--muted); }
 table { border-collapse:collapse; width:100%; margin-bottom:1.5rem; }
 th,td { text-align:left; padding:.55rem .7rem; }
 th { color:var(--muted); font-weight:500; font-size:.85rem; border-bottom:1px solid #232a34; }
 tr+tr td { border-top:1px solid #1a212b; }
 a { color:var(--fg); }
 .ok{color:var(--ok)} .bad{color:var(--bad)}
 footer { color:var(--muted); font-size:.85rem; border-top:1px solid #232a34;
   padding-top:1rem; }
</style>
</head>
<body><div class="wrap">
<h1>Backups on this storage</h1>

{{if .Rows}}
<table>
<thead><tr><th>Computer</th><th>Last reported</th><th></th></tr></thead>
<tbody>
{{range .Rows}}<tr>
 <td><a href="{{.Href}}">{{.Machine}}</a></td>
 <td class="{{if .Stale}}bad{{else}}ok{{end}}">{{.Age}}</td>
 <td class="muted">{{if .Stale}}not reporting — that computer is off, asleep, or no longer backing up{{end}}</td>
</tr>{{end}}
</tbody></table>
{{else}}<p class="muted">No computer has written a status page here yet.</p>{{end}}

<footer>
This page lists the computers that keep backups on this storage, and is built
from what is actually here rather than from what any one of them believes.
Open a computer's name for its own report. Written {{.WrittenText}}.
Paths and addresses are deliberately omitted.
</footer>
</div></body></html>
`))
