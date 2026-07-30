// SPDX-License-Identifier: MIT

// Command statuspagehtml renders the destination status page to stdout, for the
// documentation screenshot.
//
// WHY A GO PROGRAM AND NOT A FIXTURE IN PYTHON. That page is one self-contained
// file produced by internal/statuspage — there is no API to mock, and a
// hand-written copy of its HTML would be a second implementation that drifts the
// moment the real template changes. So this calls the real Render with invented
// data, exactly as mockdash.py serves the real dashboard assets with an invented
// status. The words and colours come from internal/status's shared vocabulary for
// the same reason: this tool cannot disagree with the product about what a state
// is called.
//
// The default age is three days, because what the screenshot is FOR is the
// staleness banner: a page reporting "backed up" from a machine that stopped
// reporting days ago is the one lie this program must never tell, and the page
// refuses to tell it. The banner is drawn by the page's own script from the
// written-at stamp, so backdating that stamp is all it takes to show it.
package main

import (
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/phil9922/backup-maker/internal/status"
	"github.com/phil9922/backup-maker/internal/statuspage"
)

func main() {
	age := flag.Duration("age", 72*time.Hour, "how long ago the page was written")
	fresh := flag.Bool("fresh", false, "write it as of now, with no staleness banner")
	flag.Parse()
	if *fresh {
		*age = 30 * time.Second
	}
	written := time.Now().Add(-*age)

	// One folder backed up to two places, one snapshot schedule, and a
	// destination that cannot report its capacity — which has to say so rather
	// than be left out, because silence there looks like health.
	rows := []status.Row{
		{FolderLabel: "code", TargetName: "SDCARD", State: "in sync",
			LastSeen: written.Add(-2 * time.Minute)},
		{FolderLabel: "code", TargetName: "backups", State: "in sync",
			LastSeen: written.Add(-6 * time.Minute)},
		{FolderLabel: "documents", TargetName: "SDCARD", State: "in sync",
			LastSeen: written.Add(-3 * time.Minute)},
	}
	arc := status.ArchiveRow{Name: "weekly-code", Target: "backups", State: "due",
		LastRun: written.Add(-40 * time.Hour)}

	p := statuspage.Page{Machine: "my-laptop", Written: written}
	for _, r := range rows {
		p.Rows = append(p.Rows, statuspage.Row{
			Folder: r.FolderLabel, Destination: r.TargetName,
			State:  status.RowLabel(r),
			Health: status.RowHealth(r),
			Detail: humanAgo(written.Sub(r.LastSeen)),
		})
	}
	p.Snapshots = append(p.Snapshots, statuspage.Row{
		Folder: arc.Name, Destination: arc.Target,
		State:  status.ArchiveLabel(arc),
		Health: status.ArchiveHealth(arc),
		Detail: humanAgo(written.Sub(arc.LastRun)),
	})
	p.Storage = []statuspage.StorageLine{
		{Destination: "SDCARD", Free: "52GB", Total: "64GB", UsedPct: 19},
		{Destination: "backups", Unavailable: true},
	}
	// The manual is beside the page on any destination this machine has been
	// able to write to, so a shot without it would show a destination in a state
	// almost nobody's is in. It is also the one offer on a stale page that is
	// still completely true, which is why it sits outside the dimmed section.
	p.Manual = ".backup-maker-manual/index.html"

	out, err := statuspage.Render(p)
	if err != nil {
		fmt.Fprintln(os.Stderr, "render:", err)
		os.Exit(1)
	}
	os.Stdout.Write(out)
}

// humanAgo matches the wording the daemon puts in the Detail column. Kept short
// deliberately: this is a fixture, and the daemon's own version is not exported.
func humanAgo(d time.Duration) string {
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
