// SPDX-License-Identifier: MIT

package daemon

import (
	"testing"
	"time"

	"github.com/phil9922/backup-maker/internal/config"
)

// countingDaemon is a gated daemon whose alerts land in a counter, so a test
// can assert not just what happened to the record but whether the user was
// interrupted about it.
//
// The returned function reports alerts raised SINCE IT WAS LAST CALLED and
// drains as it goes, so a test reads as a sequence of "and then nothing
// happened" rather than having to track a running total.
func countingDaemon(t *testing.T) (*daemon, func() int) {
	t.Helper()
	d := gatedDaemon(t)
	d.state.LANDevices = map[string]*config.LANDevice{}
	raised := make(chan struct{}, 256)
	a, _ := recordingAlerter(t, nil)
	a.recorded = func(config.AlertRecord) { raised <- struct{}{} }
	d.alerts = a
	return d, func() int {
		t.Helper()
		// Delivery records on its own goroutine, so give anything in flight a
		// moment to arrive before declaring that nothing did.
		time.Sleep(200 * time.Millisecond)
		n := 0
		for {
			select {
			case <-raised:
				n++
			default:
				return n
			}
		}
	}
}

// THE BUG THIS EXISTS FOR, and the reason denying is not forgetting.
//
// Denying used to DELETE the record — which deleted the only thing that
// recognised that browser. The holding page reloads itself every five seconds,
// so the device came straight back with a cookie matching nothing, was filed as
// a device nobody had ever seen, and raised a fresh alert under a fresh code.
// Turning something down therefore produced a notification every five seconds
// for as long as it kept the page open, and the only escape was switching the
// whole category off — which is how a broken backup goes unnoticed.
func TestDenyingADeviceStopsItAskingAgain(t *testing.T) {
	d, alerts := countingDaemon(t)

	_, code, _, token := d.lanDeviceSeen("", "192.168.1.60", "Android")
	if got := alerts(); got != 1 {
		t.Fatalf("the first request raised %d alerts, want exactly 1", got)
	}
	if err := d.denyLANDevice(code); err != nil {
		t.Fatalf("denying: %v", err)
	}

	// The device keeps the page open for a good while, as it would.
	for range 60 {
		approved, gotCode, _, issued := d.lanDeviceSeen(token, "192.168.1.60", "Android")
		if approved {
			t.Fatal("a denied device was admitted to the network view")
		}
		if issued != "" {
			t.Fatal("a denied device was re-issued a token: it is being treated as new, " +
				"which is what made denying alert for ever")
		}
		if gotCode != code {
			t.Fatalf("the code changed under a denied device: %q -> %q", code, gotCode)
		}
	}

	if got := alerts(); got != 0 {
		t.Errorf("sixty reloads after Deny raised %d further alerts, want 0", got)
	}
	if n := len(d.state.LANDevices); n != 1 {
		t.Errorf("the denied device left %d records, want 1 — the denial must be remembered", n)
	}
}

// And the dashboard stops offering it as a question. A row that reappears
// saying the same thing has not answered "stop asking me".
func TestADeniedDeviceIsNotStillWaitingOnTheDashboard(t *testing.T) {
	d, _ := countingDaemon(t)
	_, code, _, _ := d.lanDeviceSeen("", "192.168.1.61", "iPhone")
	if err := d.denyLANDevice(code); err != nil {
		t.Fatal(err)
	}

	devices := d.lanDevices()
	if len(devices) != 1 {
		t.Fatalf("the denied device is not listed at all: %+v — it could never be undone", devices)
	}
	if !devices[0].Denied {
		t.Error("a denied device is not reported as denied, so the panel would draw it as a live request")
	}
	if devices[0].Approved {
		t.Error("a denied device reads as approved")
	}
}

// THE TRAP. The five-minute pending expiry runs over every unapproved device,
// and a denied device is unapproved — so without an exemption the denial is
// swept away five minutes after it is given, the next reload arrives as a
// stranger, and the alerting starts again. The denial would have looked like it
// worked for exactly as long as anybody was watching.
func TestThePendingExpiryDoesNotQuietlyUndoADenial(t *testing.T) {
	d, _ := countingDaemon(t)
	now := time.Now()
	d.state.LANDevices["tok"] = &config.LANDevice{
		Code:      "ABC-123",
		FirstSeen: now.Add(-time.Hour),
		LastSeen:  now.Add(-30 * time.Minute), // long past the pending TTL
		DeniedAt:  now.Add(-30 * time.Minute),
	}

	d.expirePending(now)

	if _, still := d.state.LANDevices["tok"]; !still {
		t.Fatal("a denial was expired by the pending clock — the device would be a stranger again, " +
			"and would alert on its next reload")
	}
}

// Nor may a flood undo one. The eviction cap exists to bound what strangers can
// create; if denials counted towards it, twenty page loads from anything on the
// wifi would push out the device you turned down and it would alert again.
func TestADenialSurvivesAFloodOfNewRequests(t *testing.T) {
	d, _ := countingDaemon(t)
	_, code, _, token := d.lanDeviceSeen("", "192.168.1.62", "Android")
	if err := d.denyLANDevice(code); err != nil {
		t.Fatal(err)
	}

	for range 200 {
		d.lanDeviceSeen("", "192.168.1.99", "a device")
	}

	dev, still := d.state.LANDevices[token]
	if !still {
		t.Fatal("a denied device was evicted by a flood of new requests, undoing the denial from outside")
	}
	if dev.DeniedAt.IsZero() {
		t.Error("the record survived but the denial did not")
	}
}

// A denial lasts a week, not for ever: it is a one-click answer to something
// that interrupted you, aimed at a device you may not have identified. What
// must not lapse is the quiet in the meantime.
func TestADenialLapsesAndTheDeviceMayAskAgain(t *testing.T) {
	d, alerts := countingDaemon(t)
	_, code, _, token := d.lanDeviceSeen("", "192.168.1.63", "iPad")
	if err := d.denyLANDevice(code); err != nil {
		t.Fatal(err)
	}
	alerts() // drain the first request's alert

	// Still inside the week: silent.
	d.state.LANDevices[token].DeniedAt = time.Now().Add(-deniedLANDeviceTTL + time.Hour)
	d.lanDeviceSeen(token, "192.168.1.63", "iPad")
	if got := alerts(); got != 0 {
		t.Fatalf("a device denied less than a week ago raised %d alerts, want 0", got)
	}
	if devices := d.lanDevices(); !devices[0].Denied {
		t.Fatal("the denial lapsed early")
	}

	// Past it: the device is asking again, and this time we are told.
	d.state.LANDevices[token].DeniedAt = time.Now().Add(-deniedLANDeviceTTL - time.Minute)
	d.lanDeviceSeen(token, "192.168.1.63", "iPad")
	if got := alerts(); got != 1 {
		t.Errorf("a lapsed denial raised %d alerts on the next request, want 1", got)
	}
	devices := d.lanDevices()
	if len(devices) != 1 || devices[0].Denied {
		t.Errorf("the device is not back in the queue after its denial lapsed: %+v", devices)
	}
	if devices[0].Code != code {
		t.Errorf("the code changed across a lapse: %q -> %q", code, devices[0].Code)
	}
}

// A denial does not lapse just because the clock passed: the device has to
// actually come back and ask. Otherwise a phone denied and then left in a
// drawer climbs into the queue on its own, and the row waiting for an answer
// has nobody behind it — the exact stale-code problem the pending expiry exists
// to prevent.
func TestALapsedDenialDoesNotRejoinTheQueueUnasked(t *testing.T) {
	d, alerts := countingDaemon(t)
	now := time.Now()
	d.state.LANDevices["tok"] = &config.LANDevice{
		Code: "ABC-123", Name: "someone's tablet",
		FirstSeen: now.Add(-30 * 24 * time.Hour),
		LastSeen:  now.Add(-time.Hour), // still around, just not asking
		DeniedAt:  now.Add(-deniedLANDeviceTTL - time.Hour),
	}

	devices := d.lanDevices()
	if len(devices) != 1 || !devices[0].Denied {
		t.Errorf("a lapsed denial rejoined the queue with nobody asking: %+v", devices)
	}
	if got := alerts(); got != 0 {
		t.Errorf("polling the dashboard raised %d alerts about a device that has not asked", got)
	}
}

// A device that was denied and then stopped asking is eventually forgotten
// altogether — there is nothing left to remember once the "no" has run out and
// the thing it was aimed at has gone.
func TestADeniedDeviceThatStoppedAskingIsForgotten(t *testing.T) {
	d, _ := countingDaemon(t)
	now := time.Now()
	d.state.LANDevices["tok"] = &config.LANDevice{
		Code:      "ABC-123",
		FirstSeen: now.Add(-30 * 24 * time.Hour),
		LastSeen:  now.Add(-deniedLANDeviceTTL - time.Hour),
		DeniedAt:  now.Add(-deniedLANDeviceTTL - time.Hour),
	}

	d.expirePending(now)

	if _, still := d.state.LANDevices["tok"]; still {
		t.Error("a denied device that stopped asking a week ago is still on file")
	}
}

// Changing your mind must work at once. Somebody who denied their own tablet by
// accident should not have to wait a week, and the panel offers Allow for
// exactly that.
func TestAllowingADeviceThatWasDeniedAdmitsItImmediately(t *testing.T) {
	d, _ := countingDaemon(t)
	_, code, _, token := d.lanDeviceSeen("", "192.168.1.64", "iPhone")
	if err := d.denyLANDevice(code); err != nil {
		t.Fatal(err)
	}
	if err := d.approveLANDevice(code); err != nil {
		t.Fatalf("allowing a denied device: %v", err)
	}

	approved, _, _, _ := d.lanDeviceSeen(token, "192.168.1.64", "iPhone")
	if !approved {
		t.Fatal("a device allowed after being denied still cannot read the view")
	}
	if !d.state.LANDevices[token].DeniedAt.IsZero() {
		t.Error("approving left the denial in place; the next expiry sweep would read it as lapsed and tidy the device away")
	}
	if devices := d.lanDevices(); devices[0].Denied {
		t.Error("an allowed device is still reported as denied")
	}
}

// Revoking an APPROVED device is still a forget, and must stay one: the point
// of revoking is that the device goes back to asking, which it can only do as a
// stranger. Denying and revoking are different answers.
func TestRevokingIsStillAForgetAndNotADenial(t *testing.T) {
	d, _ := countingDaemon(t)
	_, code, _, token := d.lanDeviceSeen("", "192.168.1.65", "Mac")
	if err := d.approveLANDevice(code); err != nil {
		t.Fatal(err)
	}
	if err := d.forgetLANDevice(code); err != nil {
		t.Fatal(err)
	}
	if _, still := d.state.LANDevices[token]; still {
		t.Error("revoking left the record behind, so the device would be refused in silence " +
			"instead of being able to ask again")
	}
}
