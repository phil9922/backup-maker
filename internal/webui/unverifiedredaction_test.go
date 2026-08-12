// SPDX-License-Identifier: MIT

package webui

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/phil9922/backup-maker/internal/status"
)

// A snapshot that was written but never checked is a health fact, and the
// network view is owed it: somebody watching from the sofa can act on "that
// backup was not proved restorable". WHY it was not checked is not: the reason
// quotes the destination's own error, which names the share and the path it
// could not read back — the same reconnaissance this view strips from every
// destination's location and capacity.
func TestTheNetworkViewSaysASnapshotWasNotCheckedWithoutSayingWhere(t *testing.T) {
	m := status.Model{
		MachineName: "my-laptop",
		Archives: []status.ArchiveRow{{
			Name: "weekly-code", Target: "nas", State: "ok",
			Unverified:       true,
			UnverifiedReason: "the snapshot was written but not checked: reading it back off the destination failed: open //192.168.1.30/backups/backup-maker-archives: host is down",
			Detail:           "the snapshot was written but not checked",
		}},
	}
	raw, err := json.Marshal(RedactForNetwork(m))
	if err != nil {
		t.Fatal(err)
	}
	got := string(raw)
	for _, leak := range []string{"unverified_reason", "192.168.1.30", "host is down", "detail"} {
		if strings.Contains(got, leak) {
			t.Errorf("%q reached the network view:\n%s", leak, got)
		}
	}
	if !strings.Contains(got, `"unverified":true`) {
		t.Errorf("the network view cannot say the snapshot was never checked:\n%s", got)
	}
}
