// SPDX-License-Identifier: MIT

package daemon

import (
	"strings"
	"testing"
)

// WHAT THE NAME IS FOR. A request used to arrive as a code, a coarse guess at
// the kind of device and an address — which answers "something is asking" but
// not "what am I letting in", and in a house with three iPhones on it an
// address that moves with the DHCP lease answers nothing at all. The device is
// the only end that knows what it is, so it is the end that is asked.
func TestADeviceNamesItselfAndTheNameReachesTheDashboard(t *testing.T) {
	d, _ := countingDaemon(t)
	_, code, _, token := d.lanDeviceSeen("", "192.168.1.70", "iPhone")

	if !d.lanDeviceNamed(token, "Alex's phone") {
		t.Fatal("naming a waiting device was refused")
	}

	devices := d.lanDevices()
	if len(devices) != 1 || devices[0].Name != "Alex's phone" {
		t.Fatalf("the name did not reach the dashboard: %+v", devices)
	}
	if devices[0].Code != code {
		t.Error("naming a device changed its code")
	}

	// And it is shown back to the device, so whoever typed it can see it took.
	_, _, name, _ := d.lanDeviceSeen(token, "192.168.1.70", "iPhone")
	if name != "Alex's phone" {
		t.Errorf("the holding page would not show the name back: %q", name)
	}
}

// A DEVICE MAY NAME ITSELF AND NOTHING ELSE. The name is keyed on the token in
// the cookie rather than on the code, which is short and on screen: keyed on
// the code, anything on the wifi could rename somebody else's pending request
// by guessing at it — and a row approved because of what it says about itself
// is exactly the failure the code exists to prevent.
func TestADeviceCannotNameAnyRecordButItsOwn(t *testing.T) {
	d, _ := countingDaemon(t)
	_, victimCode, _, victim := d.lanDeviceSeen("", "192.168.1.71", "iPhone")
	if !d.lanDeviceNamed(victim, "Alex's phone") {
		t.Fatal("naming the first device failed")
	}

	// A second device on the same wifi, guessing.
	for _, guess := range []string{victimCode, strings.ToLower(victimCode), "", "token-guessed"} {
		if d.lanDeviceNamed(guess, "Alex's phone (already approved)") {
			t.Errorf("a device renamed another record using %q", guess)
		}
	}
	if got := d.lanDevices()[0].Name; got != "Alex's phone" {
		t.Errorf("the first device's name was overwritten: %q", got)
	}
}

// The name is a label, not a credential: it never decides anything, so naming
// yourself must not admit you.
func TestNamingADeviceDoesNotAdmitIt(t *testing.T) {
	d, _ := countingDaemon(t)
	_, _, _, token := d.lanDeviceSeen("", "192.168.1.72", "Android")
	d.lanDeviceNamed(token, "the household NAS")

	if approved, _, _, _ := d.lanDeviceSeen(token, "192.168.1.72", "Android"); approved {
		t.Fatal("a device admitted itself by typing a name")
	}
}

// Naming does not raise a second notification. The first one already sent the
// user to the dashboard, where the name is; interrupting them again about
// something already on the screen they were sent to is a nag.
func TestNamingADeviceDoesNotRaiseASecondAlert(t *testing.T) {
	d, alerts := countingDaemon(t)
	_, _, _, token := d.lanDeviceSeen("", "192.168.1.73", "iPad")
	if got := alerts(); got != 1 {
		t.Fatalf("the request itself raised %d alerts, want 1", got)
	}

	d.lanDeviceNamed(token, "the tablet in the kitchen")

	if got := alerts(); got != 0 {
		t.Errorf("naming raised %d further alerts, want 0", got)
	}
}

// Whatever the device sends is sanitised before it is stored, so nothing that
// reaches a log line or a list has had control characters in it since the
// moment it arrived.
func TestAStoredDeviceNameIsAlreadySanitised(t *testing.T) {
	d, _ := countingDaemon(t)
	_, _, _, token := d.lanDeviceSeen("", "192.168.1.74", "Android")

	d.lanDeviceNamed(token, "kitchen\r\nApproved: yes")

	got := d.lanDevices()[0].Name
	if strings.ContainsAny(got, "\r\n") {
		t.Errorf("a newline was stored in a device name: %q — it would forge a second line in the log", got)
	}
	if got != "kitchen Approved: yes" {
		t.Errorf("name stored as %q", got)
	}
}
