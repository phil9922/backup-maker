// SPDX-License-Identifier: MIT

package discover

import (
	"encoding/binary"
	"net"
	"strings"
	"time"
	"unicode"
)

// netbiosName sends a single NBSTAT (node status) query to ip:137 and returns
// the machine's unique workstation name, or "" on any failure. Hand-rolled to
// avoid a dependency; the packet format is fixed (RFC 1002 §4.2.17).
func netbiosName(ip string, timeout time.Duration) string {
	conn, err := net.DialTimeout("udp", net.JoinHostPort(ip, "137"), timeout)
	if err != nil {
		return ""
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(timeout))

	// NBSTAT query for the wildcard name "*".
	var q []byte
	q = binary.BigEndian.AppendUint16(q, 0x1eaf) // transaction ID (arbitrary)
	q = binary.BigEndian.AppendUint16(q, 0)      // flags: standard query
	q = binary.BigEndian.AppendUint16(q, 1)      // QDCOUNT
	q = append(q, 0, 0, 0, 0, 0, 0)              // AN/NS/AR counts
	// Encoded name: "*" padded with 0x00 to 16 bytes, half-ASCII encoding.
	name := make([]byte, 16)
	name[0] = '*'
	q = append(q, 32) // encoded length
	for _, b := range name {
		q = append(q, 'A'+(b>>4), 'A'+(b&0x0f))
	}
	q = append(q, 0)                             // name terminator
	q = binary.BigEndian.AppendUint16(q, 0x0021) // type NBSTAT
	q = binary.BigEndian.AppendUint16(q, 0x0001) // class IN

	if _, err := conn.Write(q); err != nil {
		return ""
	}
	resp := make([]byte, 1024)
	n, err := conn.Read(resp)
	if err != nil {
		return ""
	}
	return parseNBSTAT(resp[:n])
}

// parseNBSTAT pulls the unique workstation name out of a node-status response.
//
// It WALKS the packet rather than jumping to a fixed offset. The offset is not
// fixed: a response may or may not echo the question — Samba sets QDCOUNT=0
// and sends none — and the answer record carries a name of its own. Assuming
// one layout and hard-coding the arithmetic put the read 4 bytes into the
// middle of the name, so every lookup failed and every machine on the network
// was shown to the user as a bare IP address to guess at.
func parseNBSTAT(resp []byte) string {
	const headerLen = 12
	if len(resp) < headerLen {
		return ""
	}
	qdCount := int(binary.BigEndian.Uint16(resp[4:6]))
	anCount := int(binary.BigEndian.Uint16(resp[6:8]))
	if anCount < 1 {
		return ""
	}
	p := headerLen
	ok := true
	for i := 0; i < qdCount && ok; i++ {
		if p, ok = skipName(resp, p); ok {
			p += 4 // QTYPE + QCLASS
		}
	}
	// The answer record: NAME, then TYPE(2) CLASS(2) TTL(4) RDLENGTH(2).
	if p, ok = skipName(resp, p); !ok {
		return ""
	}
	p += 10
	if p >= len(resp) {
		return ""
	}
	numNames := int(resp[p])
	p++
	for i := 0; i < numNames && p+18 <= len(resp); i, p = i+1, p+18 {
		raw := resp[p : p+15]
		suffix := resp[p+15]
		flags := binary.BigEndian.Uint16(resp[p+16 : p+18])
		const groupFlag = 0x8000
		if suffix == 0x00 && flags&groupFlag == 0 { // unique workstation name
			name := strings.TrimRight(string(raw), " \x00")
			if isPrintable(name) {
				return name
			}
		}
	}
	return ""
}

// skipName steps over a name field: length-prefixed labels ending in a zero
// byte, or a two-byte compression pointer. Returns the offset just past it.
func skipName(b []byte, p int) (int, bool) {
	for {
		if p < 0 || p >= len(b) {
			return 0, false
		}
		l := int(b[p])
		if l == 0 {
			return p + 1, true
		}
		if l&0xC0 == 0xC0 { // pointer to a name earlier in the packet
			if p+1 >= len(b) {
				return 0, false
			}
			return p + 2, true
		}
		p += 1 + l
	}
}

func isPrintable(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r > unicode.MaxASCII || !unicode.IsPrint(r) {
			return false
		}
	}
	return true
}
