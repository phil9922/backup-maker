// SPDX-License-Identifier: MIT

package smbfs

import (
	"context"
	"errors"
	"net"
	"testing"

	"golang.org/x/net/dns/dnsmessage"
)

func stubLookups(t *testing.T, sys func(context.Context, string) ([]net.IPAddr, error), mdns func(context.Context, string) (net.IP, error)) {
	t.Helper()
	oldSys, oldM := lookupSystem, lookupMDNS
	t.Cleanup(func() { lookupSystem, lookupMDNS = oldSys, oldM })
	lookupSystem, lookupMDNS = sys, mdns
}

var errNoDNS = errors.New("lookup backup-pi.local on 127.0.0.53:53: server misbehaving")

// THE GUARANTEE this file exists for: a share addressed by its `.local` name is
// still found when the system resolver cannot answer for it — which is every
// static Linux release build — so a router moving the box to a new address
// does not take the destination offline.
func TestALocalNameIsFoundWhenTheSystemResolverCannotAnswer(t *testing.T) {
	stubLookups(t,
		func(context.Context, string) ([]net.IPAddr, error) { return nil, errNoDNS },
		func(_ context.Context, host string) (net.IP, error) {
			if host != "backup-pi.local" {
				t.Fatalf("asked the LAN for %q", host)
			}
			return net.IPv4(192, 168, 1, 20), nil
		})
	got, err := resolveAddr(context.Background(), "backup-pi.local:445")
	if err != nil || got != "192.168.1.20:445" {
		t.Fatalf("resolveAddr = %q, %v; want 192.168.1.20:445", got, err)
	}
}

func TestTheSystemResolverIsAskedFirstAndPrefersIPv4(t *testing.T) {
	stubLookups(t,
		func(context.Context, string) ([]net.IPAddr, error) {
			return []net.IPAddr{{IP: net.ParseIP("fd00::20")}, {IP: net.IPv4(192, 168, 1, 21)}}, nil
		},
		func(context.Context, string) (net.IP, error) {
			t.Fatal("multicast DNS used although the system resolver answered")
			return nil, nil
		})
	got, err := resolveAddr(context.Background(), "backup-pi.local:445")
	if err != nil || got != "192.168.1.21:445" {
		t.Fatalf("resolveAddr = %q, %v", got, err)
	}
}

func TestALiteralAddressIsUsedAsItIs(t *testing.T) {
	stubLookups(t,
		func(context.Context, string) ([]net.IPAddr, error) {
			t.Fatal("resolver asked about an IP")
			return nil, nil
		},
		func(context.Context, string) (net.IP, error) { t.Fatal("LAN asked about an IP"); return nil, nil })
	if got, err := resolveAddr(context.Background(), "192.168.1.20:445"); err != nil || got != "192.168.1.20:445" {
		t.Fatalf("resolveAddr = %q, %v", got, err)
	}
}

// Only `.local` goes to the LAN: an ordinary DNS name that fails is a DNS
// problem, and asking every box on the network about it would be noise.
func TestAnOrdinaryNameIsNeverAskedOverMulticast(t *testing.T) {
	stubLookups(t,
		func(context.Context, string) ([]net.IPAddr, error) { return nil, errNoDNS },
		func(context.Context, string) (net.IP, error) { t.Fatal("LAN asked about a DNS name"); return nil, nil })
	if _, err := resolveAddr(context.Background(), "nas.example.com:445"); err == nil {
		t.Fatal("an unresolvable DNS name resolved")
	}
}

func response(t *testing.T, records ...dnsmessage.Resource) []byte {
	t.Helper()
	b := dnsmessage.NewBuilder(nil, dnsmessage.Header{Response: true, Authoritative: true})
	if err := b.StartAnswers(); err != nil {
		t.Fatal(err)
	}
	for _, r := range records {
		if err := b.AResource(r.Header, *r.Body.(*dnsmessage.AResource)); err != nil {
			t.Fatal(err)
		}
	}
	msg, err := b.Finish()
	if err != nil {
		t.Fatal(err)
	}
	return msg
}

func aRecord(name string, ip [4]byte) dnsmessage.Resource {
	return dnsmessage.Resource{
		Header: dnsmessage.ResourceHeader{Name: dnsmessage.MustNewName(name), Type: dnsmessage.TypeA, Class: dnsmessage.ClassINET},
		Body:   &dnsmessage.AResource{A: ip},
	}
}

// A responder can carry records for other hosts; only the one asked for counts,
// whatever case either side spells it in.
func TestOnlyTheRecordForTheNameAskedForIsTaken(t *testing.T) {
	name := dnsmessage.MustNewName("backup-pi.local.")
	msg := response(t,
		aRecord("printer.local.", [4]byte{192, 168, 1, 9}),
		aRecord("BACKUP-PI.local.", [4]byte{192, 168, 1, 20}))
	if ip := answerFor(msg, name); !ip.Equal(net.IPv4(192, 168, 1, 20)) {
		t.Fatalf("answerFor = %v, want 192.168.1.20", ip)
	}
	if ip := answerFor(response(t, aRecord("printer.local.", [4]byte{192, 168, 1, 9})), name); ip != nil {
		t.Fatalf("took another host's address: %v", ip)
	}
}

// End to end over a real socket: the query goes out, a responder answers by
// unicast to the port it came from, and the address comes back.
func TestAMulticastQueryRoundTrips(t *testing.T) {
	srv, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()
	old := mdnsGroup
	mdnsGroup = srv.LocalAddr().(*net.UDPAddr)
	t.Cleanup(func() { mdnsGroup = old })

	go func() {
		buf := make([]byte, 1500)
		n, from, err := srv.ReadFromUDP(buf)
		if err != nil {
			return
		}
		var p dnsmessage.Parser
		h, err := p.Start(buf[:n])
		if err != nil {
			return
		}
		q, err := p.Question()
		if err != nil || q.Type != dnsmessage.TypeA || q.Class&(1<<15) == 0 {
			return // not asking for a unicast A answer: stay silent and fail
		}
		b := dnsmessage.NewBuilder(nil, dnsmessage.Header{ID: h.ID, Response: true})
		_ = b.StartAnswers()
		_ = b.AResource(dnsmessage.ResourceHeader{Name: q.Name, Type: dnsmessage.TypeA, Class: dnsmessage.ClassINET},
			dnsmessage.AResource{A: [4]byte{192, 168, 1, 20}})
		msg, _ := b.Finish()
		_, _ = srv.WriteToUDP(msg, from)
	}()

	ip, err := queryMDNS(context.Background(), "backup-pi.local")
	if err != nil || !ip.Equal(net.IPv4(192, 168, 1, 20)) {
		t.Fatalf("queryMDNS = %v, %v", ip, err)
	}
}
