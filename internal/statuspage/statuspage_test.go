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
