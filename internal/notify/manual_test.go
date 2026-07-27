// SPDX-License-Identifier: MIT

package notify

import (
	"context"
	"os"
	"testing"
)

// Not an assertion — the only way to check a notification is to look at it. Off
// unless asked for, because `go test ./...` must not pop things onto whoever's
// screen is nearest:
//
//	BACKUP_MAKER_NOTIFY_MANUAL=1 go test ./internal/notify -run Manual -v
//
// The critical one should STAY until you dismiss it; the normal one should fade
// on its own. If the critical one fades, this machine's desktop does not honour
// urgency and the sticky guarantee does not hold here.
func TestManualNotification(t *testing.T) {
	if os.Getenv("BACKUP_MAKER_NOTIFY_MANUAL") == "" {
		t.Skip("set BACKUP_MAKER_NOTIFY_MANUAL=1 to raise real notifications")
	}
	n := Desktop()
	if err := n.Notify(context.Background(), Critical,
		"Backups are not reaching nas",
		"Last seen 8 days ago. Nothing has been backed up there since."); err != nil {
		t.Errorf("critical: %v", err)
	}
	if err := n.Notify(context.Background(), Normal,
		"nas is backing up again",
		"Backups to this destination have resumed."); err != nil {
		t.Errorf("normal: %v", err)
	}
}
