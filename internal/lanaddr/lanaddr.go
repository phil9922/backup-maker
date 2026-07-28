// SPDX-License-Identifier: MIT

// Package lanaddr reports this machine's address on the local network.
//
// It exists because a dashboard bound to 127.0.0.1 is unreachable from a phone
// or another PC, and because an application cannot reserve an address for
// itself: a service binds the address its host already has. The stable-address
// problem is solved on the router with a DHCP reservation, so this package's
// job is to surface the current address and the MAC to reserve it against.
package lanaddr

import (
	"fmt"
	"net"
	"strings"
)

// Interface is one usable local network interface.
type Interface struct {
	Name string `json:"name"`
	IP   string `json:"ip"`
	// MAC is what a router's DHCP reservation is keyed on, so the dashboard
	// can show it and make setting a fixed address copy-paste.
	MAC string `json:"mac"`
	// Wired is true for ethernet. A backup destination on wifi drops off and
	// reads as offline, so the wired interface is preferred when both exist.
	Wired bool `json:"wired"`
}

// Primary returns the interface most likely to be "the LAN", preferring wired.
//
// Virtual interfaces are skipped deliberately: binding a dashboard to a Docker
// bridge or a VPN tun device would either be unreachable from the network the
// user means, or would expose it somewhere they did not intend.
func Primary() (Interface, error) {
	all, err := Usable()
	if err != nil {
		return Interface{}, err
	}
	if len(all) == 0 {
		return Interface{}, fmt.Errorf("no local network interface found")
	}
	for _, i := range all {
		if i.Wired {
			return i, nil
		}
	}
	return all[0], nil
}

// Usable lists private IPv4 interfaces that could serve a dashboard, wired
// first.
func Usable() ([]Interface, error) {
	ifaces, err := net.Interfaces()
	if err != nil {
		return nil, err
	}
	var wired, wireless []Interface
	for _, ifi := range ifaces {
		if ifi.Flags&net.FlagUp == 0 || ifi.Flags&net.FlagLoopback != 0 {
			continue
		}
		if isVirtual(ifi.Name) {
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
			ip4 := ipnet.IP.To4()
			// Private only: a globally routable address on a home machine is
			// almost always a misconfiguration, and binding to it would put
			// the dashboard somewhere the user never asked for.
			if ip4 == nil || !ip4.IsPrivate() {
				continue
			}
			e := Interface{
				Name:  ifi.Name,
				IP:    ip4.String(),
				MAC:   ifi.HardwareAddr.String(),
				Wired: isWired(ifi.Name),
			}
			if e.Wired {
				wired = append(wired, e)
			} else {
				wireless = append(wireless, e)
			}
			break // one address per interface is enough
		}
	}
	return append(wired, wireless...), nil
}

// isVirtual filters out bridges, containers and VPN tunnels by name. Name
// matching is crude but portable; the alternative is per-OS interface-type
// queries for a decision the user can override by naming an interface.
func isVirtual(name string) bool {
	n := strings.ToLower(name)
	for _, p := range []string{
		"docker", "br-", "veth", "virbr", "vmnet", "vboxnet",
		"tun", "tap", "wg", "tailscale", "zt", "utun", "ham",
	} {
		if strings.HasPrefix(n, p) {
			return true
		}
	}
	return false
}

func isWired(name string) bool {
	n := strings.ToLower(name)
	for _, p := range []string{"en", "eth", "eno", "ens", "enp"} {
		if strings.HasPrefix(n, p) {
			// macOS names wifi en0/en1 too, so exclude the known wireless
			// prefixes first below.
			return !isWireless(n)
		}
	}
	return false
}

func isWireless(name string) bool {
	for _, p := range []string{"wl", "wlan", "wlp", "wifi", "ath", "ra"} {
		if strings.HasPrefix(name, p) {
			return true
		}
	}
	return false
}

// PortFree reports whether addr:port can be bound right now.
//
// Checked before starting so a clash produces a clear message instead of the
// dashboard silently not appearing on the network.
func PortFree(ip string, port int) error {
	ln, err := net.Listen("tcp", net.JoinHostPort(ip, fmt.Sprint(port)))
	if err != nil {
		return fmt.Errorf("port %d is already in use on %s: %w", port, ip, err)
	}
	return ln.Close()
}
