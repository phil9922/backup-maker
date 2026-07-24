// SPDX-License-Identifier: MIT

package lanaddr

import (
	"net"
	"testing"
)

// Binding a dashboard to a Docker bridge or a VPN tunnel would either be
// unreachable from the network the user means, or expose it somewhere they
// never intended. Both are worse than failing to find an address.
func TestVirtualInterfacesAreSkipped(t *testing.T) {
	virtual := []string{
		"docker0", "br-1a2b3c", "veth9f2", "virbr0", "vmnet1", "vboxnet0",
		"tun0", "tap0", "wg0", "tailscale0", "zt5u4", "utun3",
	}
	for _, n := range virtual {
		if !isVirtual(n) {
			t.Errorf("%q should be treated as virtual", n)
		}
	}
	real := []string{"eth0", "enp3s0", "eno1", "wlp1s0", "wlan0", "en0"}
	for _, n := range real {
		if isVirtual(n) {
			t.Errorf("%q is a real interface but was skipped", n)
		}
	}
}

// A destination on wifi drops off and reads as offline, so wired is preferred
// when a machine has both.
func TestWiredClassification(t *testing.T) {
	for _, n := range []string{"eth0", "eno1", "enp3s0", "ens33", "en0"} {
		if !isWired(n) {
			t.Errorf("%q should classify as wired", n)
		}
	}
	// macOS names wifi en0/en1 too, but Linux wireless prefixes must never
	// be mistaken for ethernet.
	for _, n := range []string{"wlp1s0", "wlan0", "wlx001", "wifi0"} {
		if isWired(n) {
			t.Errorf("%q is wireless but classified as wired", n)
		}
		if !isWireless(n) {
			t.Errorf("%q should classify as wireless", n)
		}
	}
}

func TestPortFreeDetectsAClash(t *testing.T) {
	// Hold a port, then confirm the check notices. A silent clash would mean
	// the dashboard never appears on the network with no explanation.
	if err := PortFree("127.0.0.1", 0); err != nil {
		t.Fatalf("port 0 (any free port) should be bindable: %v", err)
	}
	ln, err := netListen(t)
	if err != nil {
		t.Skip("cannot bind a test port here")
	}
	defer ln.Close()
	if err := PortFree("127.0.0.1", portOf(ln)); err == nil {
		t.Error("a port already in use was reported as free")
	}
}

func netListen(t *testing.T) (*net.TCPListener, error) {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	return l.(*net.TCPListener), nil
}

func portOf(l *net.TCPListener) int { return l.Addr().(*net.TCPAddr).Port }
