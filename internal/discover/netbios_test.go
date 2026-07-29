// SPDX-License-Identifier: MIT

package discover

import (
	"encoding/binary"
	"encoding/hex"
	"testing"
)

// A real node-status response, captured from a Samba host on a home network
// (a Raspberry Pi named backup-pi: Debian 13, Samba 4). Kept verbatim rather
// than hand-built, because the bug this test exists for was an assumption
// about the shape of a real packet, and only a real packet can contradict one.
const realNBSTATResponse = "1eaf8400000000010000000020434b4141414141414141414141414141414141" +
	"4141414141414141414141414100002100010000000000ad074241434b55502d" +
	"50492020202020200004004241434b55502d5049202020202020030400424143" +
	"4b55502d504920202020202020040001025f5f4d5342524f5753455f5f020184" +
	"00574f524b47524f5550202020202020008400574f524b47524f555020202020" +
	"20201d0400574f524b47524f55502020202020201e8400000000000000000000" +
	"0000000000000000000000000000000000000000000000000000000000000000" +
	"0000000000"

func mustHex(t *testing.T, s string) []byte {
	t.Helper()
	b, err := hex.DecodeString(s)
	if err != nil {
		t.Fatalf("bad fixture: %v", err)
	}
	return b
}

// The guarantee: a machine that answers with its name is shown by that name.
// When this broke, every computer on the network appeared in the setup wizard
// as a bare IP address, leaving the user to guess which of their own machines
// they were about to send backups to.
func TestAHostThatAnswersIsNamedRatherThanShownAsAnAddress(t *testing.T) {
	if got := parseNBSTAT(mustHex(t, realNBSTATResponse)); got != "BACKUP-PI" {
		t.Fatalf("name = %q, want %q", got, "BACKUP-PI")
	}
}

// Samba omits the question section (QDCOUNT=0); other implementations echo it.
// Both have to work, which is why the parser walks the packet rather than
// jumping to a fixed offset — assuming one shape is what broke it.
func TestTheNameIsFoundWhetherOrNotTheQuestionIsEchoed(t *testing.T) {
	base := mustHex(t, realNBSTATResponse)
	if qd := binary.BigEndian.Uint16(base[4:6]); qd != 0 {
		t.Fatalf("fixture QDCOUNT = %d, expected the Samba shape (0)", qd)
	}

	question := []byte{32}
	for i := 0; i < 32; i++ {
		question = append(question, 'A')
	}
	question = append(question, 0)
	question = binary.BigEndian.AppendUint16(question, 0x0021)
	question = binary.BigEndian.AppendUint16(question, 0x0001)

	echoed := append([]byte{}, base[:12]...)
	binary.BigEndian.PutUint16(echoed[4:6], 1) // QDCOUNT = 1
	echoed = append(echoed, question...)
	echoed = append(echoed, base[12:]...)

	if got := parseNBSTAT(echoed); got != "BACKUP-PI" {
		t.Errorf("with the question echoed, name = %q, want %q", got, "BACKUP-PI")
	}
}

// A group name is not a machine. WORKGROUP appears in the same response and
// must never be offered as the name of a computer.
func TestGroupNamesAreNotMistakenForMachines(t *testing.T) {
	if got := parseNBSTAT(mustHex(t, realNBSTATResponse)); got == "WORKGROUP" {
		t.Fatal("the workgroup was reported as the machine's name")
	}
}

// This parses input from an unauthenticated source on the network, so a
// truncated or hostile packet must be refused rather than read past the end.
func TestAMalformedPacketIsRefusedRatherThanCrashing(t *testing.T) {
	full := mustHex(t, realNBSTATResponse)
	for i := 0; i < len(full); i++ {
		if got := parseNBSTAT(full[:i]); got != "" && got != "BACKUP-PI" {
			t.Fatalf("truncated to %d bytes produced %q", i, got)
		}
	}
	for _, bad := range [][]byte{
		nil,
		{},
		{0x00},
		make([]byte, 11),
		append(make([]byte, 12), 0x3f, 0x41, 0x41), // a name that never ends
	} {
		if got := parseNBSTAT(bad); got != "" {
			t.Errorf("malformed packet produced %q, want empty", got)
		}
	}
}
