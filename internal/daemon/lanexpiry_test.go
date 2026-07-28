// SPDX-License-Identifier: MIT

package daemon

import (
	"log/slog"
	"testing"
	"time"

	"github.com/phil9922/backup-maker/internal/config"
)

func expiryDaemon(t *testing.T) *daemon {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	t.Setenv("HOME", dir)
	t.Setenv("AppData", dir)
	t.Setenv("LOCALAPPDATA", dir)
	return &daemon{
		state: &config.State{LANDevices: map[string]*config.LANDevice{}},
		log:   slog.New(slog.DiscardHandler),
		cfg:   config.New(),
	}
}

// A request is somebody standing there holding a phone. If nobody answers, the
// entry left behind is worse than useless: a code in a list, indistinguishable
// from a live request, that somebody may later approve with no idea what they
// are admitting.
func TestAnUnansweredRequestExpires(t *testing.T) {
	d := expiryDaemon(t)
	now := time.Now()
	d.state.LANDevices["tok"] = &config.LANDevice{
		Code: "ABC-123", FirstSeen: now.Add(-10 * time.Minute), LastSeen: now.Add(-6 * time.Minute),
	}

	d.expirePending(now)

	if _, still := d.state.LANDevices["tok"]; still {
		t.Error("a request unanswered for 6 minutes is still pending")
	}
}

// APPROVED DEVICES MUST NEVER EXPIRE. That is a decision somebody made;
// silently revoking it would mean a phone that worked for weeks stops one
// morning with nothing on any screen explaining why.
func TestAnApprovedDeviceNeverExpires(t *testing.T) {
	d := expiryDaemon(t)
	now := time.Now()
	d.state.LANDevices["tok"] = &config.LANDevice{
		Code: "ABC-123", Approved: true,
		FirstSeen: now.Add(-30 * 24 * time.Hour), LastSeen: now.Add(-30 * 24 * time.Hour),
	}

	d.expirePending(now)

	if _, still := d.state.LANDevices["tok"]; !still {
		t.Fatal("an approved device was expired — it would be locked out with no explanation")
	}
}

// A phone sitting on the holding page is STILL ASKING: the page reloads itself
// every few seconds. Timing it out from under somebody watching the screen
// would be its own small betrayal, so the clock runs on LastSeen.
func TestADeviceStillWatchingIsNotExpired(t *testing.T) {
	d := expiryDaemon(t)
	now := time.Now()
	d.state.LANDevices["tok"] = &config.LANDevice{
		Code:      "ABC-123",
		FirstSeen: now.Add(-30 * time.Minute), // asked long ago...
		LastSeen:  now.Add(-2 * time.Second),  // ...but is still refreshing
	}

	d.expirePending(now)

	if _, still := d.state.LANDevices["tok"]; !still {
		t.Error("a device still refreshing the holding page was expired under it")
	}
}

// The boundary, so the TTL means what the comment says.
func TestExpiryHappensAtFiveMinutes(t *testing.T) {
	now := time.Now()
	cases := []struct {
		since time.Duration
		gone  bool
	}{
		{4 * time.Minute, false},
		{pendingLANDeviceTTL - time.Second, false},
		{pendingLANDeviceTTL + time.Second, true},
		{time.Hour, true},
	}
	for _, c := range cases {
		d := expiryDaemon(t)
		d.state.LANDevices["tok"] = &config.LANDevice{
			Code: "ABC-123", FirstSeen: now.Add(-c.since), LastSeen: now.Add(-c.since),
		}
		d.expirePending(now)
		_, still := d.state.LANDevices["tok"]
		if still == c.gone {
			t.Errorf("waiting %v: present=%v, want present=%v", c.since, still, !c.gone)
		}
	}
}

// The dashboard's own read path must expire too. It is the thing being polled,
// so it is what makes a lapsed request leave the screen on its own rather than
// sitting there looking live until something touches the network listener.
func TestTheDashboardReadPathExpiresStaleRequests(t *testing.T) {
	d := expiryDaemon(t)
	now := time.Now()
	d.state.LANDevices["stale"] = &config.LANDevice{
		Code: "OLD-111", FirstSeen: now.Add(-time.Hour), LastSeen: now.Add(-time.Hour),
	}
	d.state.LANDevices["live"] = &config.LANDevice{
		Code: "NEW-222", FirstSeen: now, LastSeen: now,
	}

	got := d.lanDevices()

	if len(got) != 1 || got[0].Code != "NEW-222" {
		t.Fatalf("dashboard listed %+v; the lapsed request should be gone", got)
	}
}

// And an expired code cannot be approved afterwards — the record is gone, so
// the approval is refused rather than admitting a device nobody can see.
func TestAnExpiredCodeCannotBeApproved(t *testing.T) {
	d := expiryDaemon(t)
	now := time.Now()
	d.state.LANDevices["tok"] = &config.LANDevice{
		Code: "ABC-123", FirstSeen: now.Add(-time.Hour), LastSeen: now.Add(-time.Hour),
	}
	d.expirePending(now)

	if err := d.approveLANDevice("ABC-123"); err == nil {
		t.Error("an expired request was approved")
	}
}
