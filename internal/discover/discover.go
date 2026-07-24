// SPDX-License-Identifier: MIT

// Package discover finds SMB file servers on the local network. Scanning is
// strictly on-demand — it runs only when the user asks (CLI `scan` or the
// dashboard button); backup-maker never listens to or probes the network in
// the background.
package discover

import (
	"context"
	"fmt"
	"net"
	"sort"
	"sync"
	"time"

	"github.com/phil9922/backup-maker/internal/smbfs"
)

// Host is one discovered SMB server.
type Host struct {
	Name      string   `json:"name"`       // NetBIOS name, DNS name, or the IP
	Addr      string   `json:"addr"`       // IP address
	Shares    []string `json:"shares"`     // listable share names (guest access)
	NeedsAuth bool     `json:"needs_auth"` // true if listing requires credentials
}

const (
	sweepWorkers   = 128
	dialTimeout    = 700 * time.Millisecond
	perHostBudget  = 3 * time.Second
	perHostWorkers = 8
	scanBudget     = 10 * time.Second
)

// Scan sweeps the local /24 subnets for SMB servers (TCP 445), resolves
// display names, and lists shares where guest access allows. Bounded to a few
// seconds; safe to call from a UI request.
func Scan(ctx context.Context) ([]Host, error) {
	ctx, cancel := context.WithTimeout(ctx, scanBudget)
	defer cancel()

	ips, err := localSubnetIPs()
	if err != nil {
		return nil, err
	}
	if len(ips) == 0 {
		return nil, fmt.Errorf("no local network detected")
	}

	responders := sweep(ctx, ips)
	hosts := make([]Host, len(responders))
	sem := make(chan struct{}, perHostWorkers)
	var wg sync.WaitGroup
	for i, addr := range responders {
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			hosts[i] = probeHost(ctx, addr)
		}()
	}
	wg.Wait()

	sort.Slice(hosts, func(i, j int) bool { return hosts[i].Addr < hosts[j].Addr })
	return hosts, nil
}

// localSubnetIPs enumerates candidate addresses: every host of each private
// IPv4 /24 this machine sits on (larger masks are capped to the local /24 to
// stay bounded).
func localSubnetIPs() ([]string, error) {
	ifaces, err := net.Interfaces()
	if err != nil {
		return nil, err
	}
	var out []string
	seen := map[string]bool{}
	for _, iface := range ifaces {
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}
		for _, a := range addrs {
			ipnet, ok := a.(*net.IPNet)
			if !ok {
				continue
			}
			ip4 := ipnet.IP.To4()
			if ip4 == nil || !ip4.IsPrivate() {
				continue
			}
			base := fmt.Sprintf("%d.%d.%d.", ip4[0], ip4[1], ip4[2])
			if seen[base] {
				continue
			}
			seen[base] = true
			self := ip4[3]
			for i := 1; i <= 254; i++ {
				if byte(i) == self {
					continue
				}
				out = append(out, fmt.Sprintf("%s%d", base, i))
			}
		}
	}
	return out, nil
}

// sweep dials ip:445 across the candidate list and returns responders.
func sweep(ctx context.Context, ips []string) []string {
	work := make(chan string)
	var mu sync.Mutex
	var found []string
	var wg sync.WaitGroup
	for range sweepWorkers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			d := net.Dialer{Timeout: dialTimeout}
			for ip := range work {
				conn, err := d.DialContext(ctx, "tcp", net.JoinHostPort(ip, "445"))
				if err != nil {
					continue
				}
				conn.Close()
				mu.Lock()
				found = append(found, ip)
				mu.Unlock()
			}
		}()
	}
	for _, ip := range ips {
		select {
		case work <- ip:
		case <-ctx.Done():
			close(work)
			wg.Wait()
			return found
		}
	}
	close(work)
	wg.Wait()
	return found
}

// probeHost resolves a display name and attempts a guest share listing.
func probeHost(ctx context.Context, addr string) Host {
	h := Host{Addr: addr, Name: resolveName(addr)}
	ctx, cancel := context.WithTimeout(ctx, perHostBudget)
	defer cancel()
	shares, err := smbfs.ListShares(ctx, addr, "", "")
	if err != nil {
		h.NeedsAuth = true
		return h
	}
	h.Shares = shares
	return h
}

// resolveName tries NetBIOS, then reverse DNS, then falls back to the IP.
func resolveName(addr string) string {
	if name := netbiosName(addr, time.Second); name != "" {
		return name
	}
	if names, err := net.LookupAddr(addr); err == nil && len(names) > 0 {
		n := names[0]
		for len(n) > 0 && n[len(n)-1] == '.' {
			n = n[:len(n)-1]
		}
		return n
	}
	return addr
}
