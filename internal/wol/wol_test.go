// SPDX-License-Identifier: MIT

package wol

import (
	"bytes"
	"io"
	"log/slog"
	"net"
	"testing"
	"time"
)

func TestParseMAC(t *testing.T) {
	want := net.HardwareAddr{0xaa, 0xbb, 0xcc, 0xdd, 0xee, 0xff}
	for _, in := range []string{
		"aa:bb:cc:dd:ee:ff",
		"AA:BB:CC:DD:EE:FF",
		"aa-bb-cc-dd-ee-ff",
		"aabb.ccdd.eeff",
		"aabbccddeeff",
		"  aa:bb:cc:dd:ee:ff  ",
	} {
		got, err := ParseMAC(in)
		if err != nil {
			t.Errorf("ParseMAC(%q): %v", in, err)
			continue
		}
		if !bytes.Equal(got, want) {
			t.Errorf("ParseMAC(%q) = %s, want %s", in, got, want)
		}
	}

	for _, in := range []string{
		"",
		"not-a-mac",
		"aa:bb:cc:dd:ee",          // too short
		"aa:bb:cc:dd:ee:ff:00:11", // EUI-64: valid MAC, unusable for WoL
		"zz:bb:cc:dd:ee:ff",       // non-hex
		"aabbccddeeffaa",          // wrong length bare hex
	} {
		if _, err := ParseMAC(in); err == nil {
			t.Errorf("ParseMAC(%q) succeeded, want error", in)
		}
	}
}

func TestMagicPacket(t *testing.T) {
	mac := net.HardwareAddr{0x01, 0x02, 0x03, 0x04, 0x05, 0x06}
	pkt := MagicPacket(mac)

	if len(pkt) != 102 {
		t.Fatalf("packet length = %d, want 102", len(pkt))
	}
	for i := 0; i < 6; i++ {
		if pkt[i] != 0xFF {
			t.Fatalf("byte %d = %#x, want 0xFF", i, pkt[i])
		}
	}
	for rep := 0; rep < 16; rep++ {
		off := 6 + rep*6
		if !bytes.Equal(pkt[off:off+6], mac) {
			t.Fatalf("repetition %d = % x, want % x", rep, pkt[off:off+6], mac)
		}
	}
}

func TestBroadcastOf(t *testing.T) {
	cases := []struct {
		cidr string
		want string
	}{
		{"192.168.1.10/24", "192.168.1.255"},
		{"10.0.0.5/8", "10.255.255.255"},
		{"172.16.4.9/20", "172.16.15.255"},
		{"192.168.1.10/32", ""}, // single host: no broadcast
	}
	for _, c := range cases {
		_, ipnet, err := net.ParseCIDR(c.cidr)
		if err != nil {
			t.Fatal(err)
		}
		ip, _, _ := net.ParseCIDR(c.cidr)
		ipnet.IP = ip // keep the host bits, as interface addrs do
		if got := broadcastOf(ipnet); got != c.want {
			t.Errorf("broadcastOf(%s) = %q, want %q", c.cidr, got, c.want)
		}
	}
}

func TestBroadcastAddrsIncludesGlobalFallback(t *testing.T) {
	addrs := BroadcastAddrs()
	if len(addrs) == 0 {
		t.Fatal("no broadcast addresses at all")
	}
	if addrs[len(addrs)-1] != "255.255.255.255" {
		t.Errorf("global broadcast should be the last fallback, got %v", addrs)
	}
	for _, a := range addrs {
		if net.ParseIP(a) == nil {
			t.Errorf("not a valid IP: %q", a)
		}
	}
}

// A magic packet is a real UDP datagram; bind a socket and prove the bytes on
// the wire are what a network card expects.
func TestSendDeliversMagicPacket(t *testing.T) {
	conn, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 0})
	if err != nil {
		t.Skipf("cannot bind loopback UDP: %v", err)
	}
	defer conn.Close()
	port := conn.LocalAddr().(*net.UDPAddr).Port

	// Send to the loopback address on the port we're listening on, rather
	// than a broadcast, so the test doesn't depend on the host's network.
	mac := net.HardwareAddr{0xde, 0xad, 0xbe, 0xef, 0x00, 0x01}
	origPorts := Ports
	Ports = []int{port}
	defer func() { Ports = origPorts }()

	if err := Send(mac, "127.0.0.1"); err != nil {
		t.Fatalf("Send: %v", err)
	}

	buf := make([]byte, 256)
	conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	n, _, err := conn.ReadFrom(buf)
	if err != nil {
		t.Fatalf("no packet received: %v", err)
	}
	if !bytes.Equal(buf[:n], MagicPacket(mac)) {
		t.Errorf("received % x, want % x", buf[:n], MagicPacket(mac))
	}
}

func TestSendRejectsBadMAC(t *testing.T) {
	if err := Send(net.HardwareAddr{1, 2, 3}, "127.0.0.1"); err == nil {
		t.Error("Send accepted a 3-byte MAC")
	}
}

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func TestWakerRateLimits(t *testing.T) {
	w := NewWaker(5*time.Minute, discardLogger())
	start := time.Date(2026, 7, 22, 10, 0, 0, 0, time.UTC)

	// Point at loopback so no packet escapes onto a real network.
	origPorts := Ports
	Ports = []int{9}
	defer func() { Ports = origPorts }()

	sent, err := w.Wake("nas", "aa:bb:cc:dd:ee:ff", "127.0.0.1", start)
	if err != nil || !sent {
		t.Fatalf("first wake: sent=%v err=%v", sent, err)
	}

	// Inside the window: suppressed.
	sent, err = w.Wake("nas", "aa:bb:cc:dd:ee:ff", "127.0.0.1", start.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if sent {
		t.Error("second wake within the rate-limit window was sent")
	}

	// A different target is tracked independently.
	sent, _ = w.Wake("omen", "aa:bb:cc:dd:ee:00", "127.0.0.1", start.Add(time.Minute))
	if !sent {
		t.Error("a different target should not be rate-limited by the first")
	}

	// Past the window: allowed again.
	sent, err = w.Wake("nas", "aa:bb:cc:dd:ee:ff", "127.0.0.1", start.Add(6*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if !sent {
		t.Error("wake after the rate-limit window was suppressed")
	}
}

func TestWakerForgetBypassesRateLimit(t *testing.T) {
	w := NewWaker(time.Hour, discardLogger())
	start := time.Date(2026, 7, 22, 10, 0, 0, 0, time.UTC)
	origPorts := Ports
	Ports = []int{9}
	defer func() { Ports = origPorts }()

	if sent, _ := w.Wake("nas", "aa:bb:cc:dd:ee:ff", "127.0.0.1", start); !sent {
		t.Fatal("first wake not sent")
	}
	if sent, _ := w.Wake("nas", "aa:bb:cc:dd:ee:ff", "127.0.0.1", start.Add(time.Second)); sent {
		t.Fatal("expected rate limiting")
	}

	w.Forget("nas")
	if sent, _ := w.Wake("nas", "aa:bb:cc:dd:ee:ff", "127.0.0.1", start.Add(2*time.Second)); !sent {
		t.Error("Forget should allow an immediate manual wake")
	}
}

func TestWakerRejectsBadMAC(t *testing.T) {
	w := NewWaker(time.Minute, discardLogger())
	if _, err := w.Wake("nas", "nonsense", "", time.Now()); err == nil {
		t.Error("Wake accepted an invalid MAC")
	}
	if !w.LastSent("nas").IsZero() {
		t.Error("a rejected MAC should not consume the rate-limit slot")
	}
}
