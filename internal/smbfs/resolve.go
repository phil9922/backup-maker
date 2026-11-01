// SPDX-License-Identifier: MIT

package smbfs

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"
	"time"

	smb2 "github.com/cloudsoda/go-smb2"
	"golang.org/x/net/dns/dnsmessage"
)

// WHY THIS FILE EXISTS. A share addressed by IP stops working the day the
// router hands that box a different address, and nothing says why: the
// destination just goes offline. That cost a week of backups once, when a
// router moved a Raspberry Pi from .20 to .37 and nobody had access to the
// router to pin it. The durable answer is to address the box by the name it
// announces itself, `backup-pi.local`, which follows it whatever its address.
//
// But the release builds are static (CGO_ENABLED=0), and on Linux Go's own
// resolver does not speak multicast DNS: it asks the system DNS server, which
// knows nothing about `.local`. So a `.local` name that the system resolver
// cannot answer is asked for here, directly on the LAN.

// mdnsTimeout bounds one multicast query. A responder on the same LAN answers
// in milliseconds; the second attempt covers one lost packet.
const (
	mdnsTimeout  = 750 * time.Millisecond
	mdnsAttempts = 2
)

var mdnsGroup = &net.UDPAddr{IP: net.IPv4(224, 0, 0, 251), Port: 5353}

// lookupMDNS is swapped by tests; the real one queries the LAN.
var lookupMDNS = queryMDNS

// lookupSystem is swapped by tests; the real one is the platform resolver.
var lookupSystem = func(ctx context.Context, host string) ([]net.IPAddr, error) {
	return net.DefaultResolver.LookupIPAddr(ctx, host)
}

// resolveAddr turns host:port into ip:port. A literal IP is returned as it is;
// a name goes to the system resolver first, and a `.local` name it cannot
// answer is asked over multicast DNS.
func resolveAddr(ctx context.Context, addr string) (string, error) {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return "", err
	}
	if net.ParseIP(host) != nil {
		return addr, nil
	}
	ips, sysErr := lookupSystem(ctx, host)
	if ip := preferIPv4(ips); sysErr == nil && ip != nil {
		return net.JoinHostPort(ip.String(), port), nil
	}
	if !isLocalName(host) {
		if sysErr == nil {
			sysErr = fmt.Errorf("no address for %s", host)
		}
		return "", sysErr
	}
	ip, err := lookupMDNS(ctx, host)
	if err != nil {
		return "", fmt.Errorf("looking up %s on the local network: %w", host, err)
	}
	return net.JoinHostPort(ip.String(), port), nil
}

func isLocalName(host string) bool {
	return strings.HasSuffix(strings.ToLower(strings.TrimSuffix(host, ".")), ".local")
}

func preferIPv4(ips []net.IPAddr) net.IP {
	for _, a := range ips {
		if v4 := a.IP.To4(); v4 != nil {
			return v4
		}
	}
	if len(ips) > 0 {
		return ips[0].IP
	}
	return nil
}

// dial opens an SMB session to addr (host:port). The TCP connection goes to the
// resolved IP under ctx's deadline; the session is still told the NAME, because
// go-smb2 builds the \\host\share tree path from it.
//
// It also replaces smb2.Dialer.Dial, which calls net.Dial with no timeout and
// ignores ctx — so dialTimeout never bounded the connect at all, and a host that
// silently dropped packets held the engine for the kernel's full SYN timeout.
func dial(ctx context.Context, d *smb2.Dialer, addr string) (*smb2.Session, error) {
	target, err := resolveAddr(ctx, addr)
	if err != nil {
		return nil, err
	}
	var nd net.Dialer
	conn, err := nd.DialContext(ctx, "tcp", target)
	if err != nil {
		return nil, fmt.Errorf("establishing TCP connection: %w", err)
	}
	sess, err := d.DialConn(ctx, conn, addr)
	if err != nil {
		conn.Close()
		return nil, err
	}
	return sess, nil
}

// queryMDNS asks the LAN for host's IPv4 address with a one-shot multicast DNS
// query (RFC 6762 §5.1): sent from an ephemeral port, so responders answer by
// unicast straight back to it, and nothing has to bind 5353 — which avahi or
// Bonjour usually holds already.
func queryMDNS(ctx context.Context, host string) (net.IP, error) {
	name, err := dnsmessage.NewName(strings.TrimSuffix(host, ".") + ".")
	if err != nil {
		return nil, err
	}
	conn, err := net.ListenUDP("udp4", nil)
	if err != nil {
		return nil, err
	}
	defer conn.Close()

	for attempt := 0; attempt < mdnsAttempts; attempt++ {
		id := uint16(time.Now().UnixNano())
		q, err := buildQuery(id, name)
		if err != nil {
			return nil, err
		}
		if _, err := conn.WriteToUDP(q, mdnsGroup); err != nil {
			return nil, err
		}
		deadline := time.Now().Add(mdnsTimeout)
		if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
			deadline = d
		}
		_ = conn.SetReadDeadline(deadline)
		buf := make([]byte, 9000)
		for {
			n, _, err := conn.ReadFromUDP(buf)
			if err != nil {
				break // deadline: try again, or give up
			}
			if ip := answerFor(buf[:n], name); ip != nil {
				return ip, nil
			}
		}
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
	}
	return nil, errors.New("nothing on the network answered to that name")
}

func buildQuery(id uint16, name dnsmessage.Name) ([]byte, error) {
	b := dnsmessage.NewBuilder(nil, dnsmessage.Header{ID: id})
	if err := b.StartQuestions(); err != nil {
		return nil, err
	}
	if err := b.Question(dnsmessage.Question{
		Name: name,
		Type: dnsmessage.TypeA,
		// Top bit set: "unicast response wanted" (RFC 6762 §5.4).
		Class: dnsmessage.ClassINET | 1<<15,
	}); err != nil {
		return nil, err
	}
	return b.Finish()
}

// answerFor returns the A record for name in a response, or nil. A responder
// may add unrelated records, and the answer may sit in either the answer or the
// additional section, so both are searched and every record is checked by name.
func answerFor(msg []byte, name dnsmessage.Name) net.IP {
	var p dnsmessage.Parser
	h, err := p.Start(msg)
	if err != nil || !h.Response {
		return nil
	}
	if err := p.SkipAllQuestions(); err != nil {
		return nil
	}
	answers, err := p.AllAnswers()
	if err != nil {
		return nil
	}
	if err := p.SkipAllAuthorities(); err == nil {
		if extra, err := p.AllAdditionals(); err == nil {
			answers = append(answers, extra...)
		}
	}
	for _, rr := range answers {
		a, ok := rr.Body.(*dnsmessage.AResource)
		if ok && strings.EqualFold(rr.Header.Name.String(), name.String()) {
			return net.IP(append([]byte(nil), a.A[:]...))
		}
	}
	return nil
}
