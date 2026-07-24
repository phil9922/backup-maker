// SPDX-License-Identifier: MIT

// Package wol sends Wake-on-LAN "magic packets" to sleeping backup targets.
//
// A magic packet is a UDP datagram containing six 0xFF bytes followed by the
// target's MAC address repeated sixteen times. A network card listening for
// it powers the machine back on. The packet is broadcast on the local network
// only — it is never routed off-LAN, so this stays consistent with the
// lan-only network mode.
//
// Waking is best-effort by nature: the packet is fire-and-forget UDP with no
// acknowledgement, the target may not have WoL enabled in firmware, and wifi
// adapters usually drop the ability entirely once the machine sleeps. Callers
// must treat a successful Send as "asked politely", never as "target is up".
package wol

import (
	"encoding/hex"
	"fmt"
	"log/slog"
	"net"
	"strings"
	"sync"
	"time"
)

// Ports are the two conventional Wake-on-LAN destination ports. Adapters vary
// in which they listen on, and the packet is tiny, so we send to both.
var Ports = []int{9, 7}

// ParseMAC accepts the common hardware-address spellings — "aa:bb:cc:dd:ee:ff",
// "aa-bb-cc-dd-ee-ff", "aabb.ccdd.eeff", or bare "aabbccddeeff" — and requires
// a 6-byte (EUI-48) result, the only size a magic packet can carry.
func ParseMAC(s string) (net.HardwareAddr, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil, fmt.Errorf("empty MAC address")
	}
	if mac, err := net.ParseMAC(s); err == nil {
		if len(mac) != 6 {
			return nil, fmt.Errorf("MAC %q must be 6 bytes for Wake-on-LAN", s)
		}
		return mac, nil
	}
	// Bare hex, with or without separators net.ParseMAC rejects.
	clean := strings.NewReplacer(":", "", "-", "", ".", "", " ", "").Replace(s)
	if len(clean) != 12 {
		return nil, fmt.Errorf("not a MAC address: %q (want aa:bb:cc:dd:ee:ff)", s)
	}
	raw, err := hex.DecodeString(clean)
	if err != nil {
		return nil, fmt.Errorf("not a MAC address: %q (want aa:bb:cc:dd:ee:ff)", s)
	}
	return net.HardwareAddr(raw), nil
}

// MagicPacket builds the 102-byte payload for mac.
func MagicPacket(mac net.HardwareAddr) []byte {
	pkt := make([]byte, 0, 6+16*6)
	for i := 0; i < 6; i++ {
		pkt = append(pkt, 0xFF)
	}
	for i := 0; i < 16; i++ {
		pkt = append(pkt, mac...)
	}
	return pkt
}

// Send broadcasts a magic packet for mac. If broadcast is empty the packet
// goes to every broadcast address this machine can see (plus the global
// 255.255.255.255), which is what makes it work on a multi-homed host without
// the user having to know their subnet.
//
// It reports success if at least one datagram left the machine; individual
// interface failures are normal (an interface may be down, or refuse
// broadcast) and are not surfaced as errors.
func Send(mac net.HardwareAddr, broadcast string) error {
	if len(mac) != 6 {
		return fmt.Errorf("MAC must be 6 bytes, got %d", len(mac))
	}
	pkt := MagicPacket(mac)

	var dests []string
	if broadcast != "" {
		dests = []string{broadcast}
	} else {
		dests = BroadcastAddrs()
	}

	var sent int
	var lastErr error
	for _, host := range dests {
		for _, port := range Ports {
			addr := net.JoinHostPort(host, fmt.Sprint(port))
			conn, err := net.DialTimeout("udp4", addr, 2*time.Second)
			if err != nil {
				lastErr = err
				continue
			}
			_, err = conn.Write(pkt)
			conn.Close()
			if err != nil {
				lastErr = err
				continue
			}
			sent++
		}
	}
	if sent == 0 {
		if lastErr == nil {
			lastErr = fmt.Errorf("no usable broadcast address")
		}
		return fmt.Errorf("sending magic packet: %w", lastErr)
	}
	return nil
}

// BroadcastAddrs lists the IPv4 broadcast addresses reachable from this host,
// most specific first, with the global broadcast last as a fallback. Directed
// subnet broadcasts are tried first because some switches and adapters ignore
// 255.255.255.255.
func BroadcastAddrs() []string {
	var out []string
	seen := map[string]bool{}
	add := func(s string) {
		if s != "" && !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}

	ifaces, err := net.Interfaces()
	if err == nil {
		for _, ifi := range ifaces {
			if ifi.Flags&net.FlagUp == 0 ||
				ifi.Flags&net.FlagLoopback != 0 ||
				ifi.Flags&net.FlagBroadcast == 0 {
				continue
			}
			addrs, err := ifi.Addrs()
			if err != nil {
				continue
			}
			for _, a := range addrs {
				ipnet, ok := a.(*net.IPNet)
				if !ok {
					continue
				}
				add(broadcastOf(ipnet))
			}
		}
	}
	add("255.255.255.255")
	return out
}

// broadcastOf returns the IPv4 broadcast address of ipnet, or "" if ipnet
// isn't a usable IPv4 network. A /32 has no meaningful broadcast address.
func broadcastOf(ipnet *net.IPNet) string {
	ip4 := ipnet.IP.To4()
	if ip4 == nil {
		return ""
	}
	mask := ipnet.Mask
	if len(mask) == net.IPv6len {
		mask = mask[12:] // IPv4-in-IPv6 mask; keep the last four bytes
	}
	if len(mask) != net.IPv4len {
		return ""
	}
	if ones, bits := ipnet.Mask.Size(); bits > 0 && ones == bits {
		return "" // /32: broadcast would just be the host itself
	}
	b := make(net.IP, net.IPv4len)
	for i := 0; i < net.IPv4len; i++ {
		b[i] = ip4[i] | ^mask[i]
	}
	return b.String()
}

// DefaultMinInterval throttles repeat wakes. A sleeping machine needs tens of
// seconds to boot and rejoin the network; re-broadcasting every poll would
// spam the LAN and could fight a machine that is deliberately shutting down.
const DefaultMinInterval = 5 * time.Minute

// Waker sends magic packets on behalf of named targets, rate-limited per
// target. The zero value is not usable; call NewWaker.
type Waker struct {
	minInterval time.Duration
	log         *slog.Logger

	mu   sync.Mutex
	last map[string]time.Time
}

func NewWaker(minInterval time.Duration, log *slog.Logger) *Waker {
	if minInterval <= 0 {
		minInterval = DefaultMinInterval
	}
	return &Waker{
		minInterval: minInterval,
		log:         log.With("sub", "wol"),
		last:        map[string]time.Time{},
	}
}

// Wake sends a magic packet for target unless one was sent within the
// rate-limit window. It reports whether a packet was actually sent.
func (w *Waker) Wake(target, mac, broadcast string, now time.Time) (bool, error) {
	hw, err := ParseMAC(mac)
	if err != nil {
		return false, err
	}

	w.mu.Lock()
	if last, ok := w.last[target]; ok && now.Sub(last) < w.minInterval {
		w.mu.Unlock()
		return false, nil
	}
	w.last[target] = now
	w.mu.Unlock()

	if err := Send(hw, broadcast); err != nil {
		w.log.Warn("wake-on-lan send failed", "target", target, "mac", mac, "err", err)
		return false, err
	}
	w.log.Info("sent wake-on-lan packet", "target", target, "mac", mac)
	return true, nil
}

// LastSent reports when a packet was last sent for target, zero if never.
func (w *Waker) LastSent(target string) time.Time {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.last[target]
}

// Forget drops a target's rate-limit history, so the next Wake sends
// immediately. Used when the user asks for a manual wake.
func (w *Waker) Forget(target string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	delete(w.last, target)
}
