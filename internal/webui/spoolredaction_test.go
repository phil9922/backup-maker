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
// names a directory on this machine's own disk and says how much room is left
// on it, which is the same reconnaissance this view strips from every
// destination's location and capacity.
func TestTheNetworkViewSaysASnapshotWasNotCheckedWithoutSayingWhere(t *testing.T) {
	m := status.Model{
		MachineName: "my-laptop",
		Archives: []status.ArchiveRow{{
			Name: "weekly-code", Target: "nas", State: "ok",
			Unverified:       true,
			UnverifiedReason: "the snapshot was written but not checked: reading it back needs about 57.0GB in /home/alex/spool and 2.0GB must stay free there, but only 34.0GB is left",
			Detail:           "the snapshot was written but not checked",
		}},
	}
	raw, err := json.Marshal(RedactForNetwork(m))
	if err != nil {
		t.Fatal(err)
	}
	got := string(raw)
	for _, leak := range []string{"unverified_reason", "/home/alex/spool", "34.0GB", "detail"} {
		if strings.Contains(got, leak) {
			t.Errorf("%q reached the network view:\n%s", leak, got)
		}
	}
	if !strings.Contains(got, `"unverified":true`) {
		t.Errorf("the network view cannot say the snapshot was never checked:\n%s", got)
	}
}
