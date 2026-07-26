// SPDX-License-Identifier: MIT

package statuspage

import (
	"strings"
	"testing"
	"time"
)

func render(t *testing.T, p Page) string {
	t.Helper()
	b, err := Render(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// The page sits on shared storage that anything on the network can read, so it
// must carry health, not a map of the machine.
func TestPageCarriesNoPathsOrAddresses(t *testing.T) {
	out := render(t, Page{
		Machine: "workstation",
		Written: time.Now(),
		Rows: []Row{{
			Folder: "code", Destination: "nas", State: "in sync", Detail: "2 minutes ago",
		}},
	})
	for _, secret := range []string{"/home/", "//192.168", "C:\\", "AAAAAAA-"} {
		if strings.Contains(out, secret) {
			t.Errorf("page leaked %q", secret)
		}
	}
	for _, want := range []string{"workstation", "code", "nas", "in sync"} {
		if !strings.Contains(out, want) {
			t.Errorf("page is missing %q; it still has to be useful", want)
		}
	}
}

// A page that reports "all in sync" from a machine that died last week is
// false reassurance — the one thing a backup tool must never give.
func TestPageCarriesTheMachineryToDetectItsOwnStaleness(t *testing.T) {
	out := render(t, Page{Machine: "workstation", Written: time.Now()})

	if !strings.Contains(out, "out of date") {
		t.Error("no staleness warning is present in the page at all")
	}
	if !strings.Contains(out, "last reported") {
		t.Error("the page must lead with how long ago it was written")
	}
	// Age is computed in the viewer's browser, because the file may be read
	// days later with nothing running to update it.
	if !strings.Contains(out, "setInterval") || !strings.Contains(out, "Date.now()") {
		t.Error("staleness must be computed at view time, not baked in at write time")
	}
}

// It has to open straight off a file share with no web server.
func TestPageIsSelfContained(t *testing.T) {
	out := render(t, Page{Machine: "m", Written: time.Now()})
	for _, external := range []string{"<link", "src=\"http", "@import", "cdn."} {
		if strings.Contains(out, external) {
			t.Errorf("page pulls an external resource (%q); it must work offline", external)
		}
	}
}

// A machine name is user-supplied and ends up in HTML.
func TestPageEscapesMachineName(t *testing.T) {
	out := render(t, Page{Machine: `<script>alert(1)</script>`, Written: time.Now()})
	if strings.Contains(out, "<script>alert(1)</script>") {
		t.Error("machine name was not escaped into the page")
	}
}

// Capacity ("is the NAS filling up?") is health, not a map of the machine, so
// it belongs on the page — but only when there is a reading to show.
func TestPageRendersStorageWhenPresent(t *testing.T) {
	out := render(t, Page{
		Machine: "workstation",
		Written: time.Now(),
		Storage: []StorageLine{{Destination: "nas", Free: "300GB", Total: "1TB", UsedPct: 70}},
	})
	for _, want := range []string{"Storage", "nas", "300GB", "1TB", "70%"} {
		if !strings.Contains(out, want) {
			t.Errorf("storage table is missing %q", want)
		}
	}
}

// A destination that could not report its capacity must still appear, saying
// so. Leaving it out is how an unprotected destination came to look identical
// to a healthy one, and the reserve is not being enforced there.
func TestPageSaysWhenSpaceCannotBeRead(t *testing.T) {
	out := render(t, Page{
		Machine: "workstation",
		Written: time.Now(),
		Storage: []StorageLine{
			{Destination: "nas", Unavailable: true},
			{Destination: "sdcard", Free: "8GB", Total: "64GB", UsedPct: 87},
		},
	})
	if !strings.Contains(out, "nas") {
		t.Error("the unmeasurable destination was left off the page entirely")
	}
	if !strings.Contains(out, "free space unavailable — reserve not enforced") {
		t.Error("the page does not say why the destination has no figures")
	}
	// The measured destination is untouched, and no invented "0%" appears
	// against the one that could not answer.
	if !strings.Contains(out, "87%") {
		t.Error("a measured destination lost its figures")
	}
	if strings.Contains(out, ">0%<") {
		t.Error("an unmeasurable destination was rendered as 0% used")
	}
}

func TestPageOmitsStorageWhenEmpty(t *testing.T) {
	out := render(t, Page{Machine: "m", Written: time.Now()})
	if strings.Contains(out, ">Storage<") {
		t.Error("an empty storage list should not render a Storage heading")
	}
}

func TestPageHandlesNothingConfigured(t *testing.T) {
	out := render(t, Page{Machine: "m", Written: time.Now()})
	if !strings.Contains(out, "No folders are being backed up") {
		t.Error("an empty setup should say so rather than showing a blank table")
	}
}
