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
	resp = resp[:n]

	// Skip the 12-byte header and the echoed question-length name (34 bytes)
	// + type/class (4), then RR fixed part (type/class/ttl/rdlength = 10).
	const off = 12 + 34 + 4 + 10
	if len(resp) < off+1 {
		return ""
	}
	numNames := int(resp[off])
	p := off + 1
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
