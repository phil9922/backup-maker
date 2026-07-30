// SPDX-License-Identifier: MIT

package setup

import (
	"testing"

	"github.com/phil9922/backup-maker/internal/localmirror"
)

func TestScanRootsFindsManifests(t *testing.T) {
	withManifest := t.TempDir()
	empty := t.TempDir()
	missing := withManifest + "-does-not-exist"

	if err := WriteManifest(localmirror.NewLocalFS(withManifest), sampleConfig(),
		map[string]string{"usb": "uuid-usb"}, "install-oldbox", "usb"); err != nil {
		t.Fatal(err)
	}

	got := scanRoots([]string{empty, missing, withManifest})
	if len(got) != 1 {
		t.Fatalf("got %d candidates, want 1: %+v", len(got), got)
	}
	c := got[0]
	if c.Path != withManifest || c.MachineName != "oldbox" {
		t.Errorf("candidate = %+v", c)
	}
	// Two targets, though this manifest describes only one of them: a scan
	// reports how many destinations the machine had, and a summarised entry
	// still answers that. Counting only the locatable one would tell somebody
	// looking at a drive that they had fewer destinations than they did.
	if c.Folders != 1 || c.Targets != 2 || c.Archives != 1 {
		t.Errorf("counts = %d/%d/%d, want 1/2/1", c.Folders, c.Targets, c.Archives)
	}
}
