// SPDX-License-Identifier: MIT

package lanaddr

import "testing"

// Not an assertion — a readout of what this machine actually looks like, so
// interface classification can be eyeballed on real hardware.
func TestReportThisMachine(t *testing.T) {
	all, err := Usable()
	if err != nil {
		t.Fatal(err)
	}
	for _, i := range all {
		t.Logf("%-10s %-15s mac=%s wired=%v", i.Name, i.IP, i.MAC, i.Wired)
	}
	p, err := Primary()
	if err != nil {
		t.Skipf("no LAN interface here: %v", err)
	}
	t.Logf("primary: %s (%s) wired=%v", p.IP, p.Name, p.Wired)
	if err := PortFree(p.IP, 8667); err != nil {
		t.Logf("8667 busy: %v", err)
	} else {
		t.Log("8667 is free")
	}
}
