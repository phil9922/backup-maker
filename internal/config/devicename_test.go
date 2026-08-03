// SPDX-License-Identifier: MIT

package config

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// EVERY CASE HERE ASSUMES A HOSTILE STRING. A device name is the one field in
// the whole program typed by somebody who has not been approved to do anything
// — it arrives on the unauthenticated network listener — and it is displayed
// next to an Approve button. It cannot be allowed to be anything but a short
// piece of visible text.
func TestADeviceNameIsReducedToPlainShortText(t *testing.T) {
	for _, tc := range []struct {
		name string
		in   string
		want string
	}{
		{"an ordinary name is kept", "Alex's phone", "Alex's phone"},
		{"surrounding space is trimmed", "   the tablet   ", "the tablet"},
		{"runs of space collapse, so nothing can pad itself across a row",
			"the\t\t   kitchen     tablet", "the kitchen tablet"},
		{"a newline cannot forge a second line in a list or a log",
			"kitchen\nApproved: yes", "kitchen Approved: yes"},
		{"control characters go", "tab\x07let\x00", "tablet"},
		{"a name split over two lines is one line, not two words run together",
			"kitchen\ntablet", "kitchen tablet"},
		{"a right-to-left override cannot reorder what is around it",
			"phone\u202egnitseT", "phonegnitseT"},
		{"zero-width characters cannot hide inside a name",
			"pho\u200bne\ufeff", "phone"},
		{"nothing but whitespace is no name at all", " \t\n ", ""},
		{"empty is a valid answer: clearing a name is allowed", "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := SafeDeviceName(tc.in); got != tc.want {
				t.Errorf("SafeDeviceName(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// A name is a label. Nothing about this endpoint may scale with what the client
// decides to send, and a row on a dashboard cannot be a paragraph.
func TestADeviceNameIsBounded(t *testing.T) {
	got := SafeDeviceName(strings.Repeat("na", 500))
	if n := utf8.RuneCountInString(got); n > 40 {
		t.Errorf("a 1000-character name was stored as %d characters", n)
	}

	// The bound counts CHARACTERS, not bytes — otherwise a name in a
	// non-Latin script would be cut to a third of the length of an English
	// one, and cut mid-rune at that.
	emoji := SafeDeviceName(strings.Repeat("日", 100))
	if n := utf8.RuneCountInString(emoji); n != 40 {
		t.Errorf("a name of wide characters was cut to %d characters, want 40", n)
	}
	if !utf8.ValidString(emoji) {
		t.Error("the cut landed inside a rune")
	}
}

// Bytes that are not text at all are not a name. Storing them would put
// invalid UTF-8 into state.json, which is JSON, which cannot hold it.
func TestBytesThatAreNotTextAreNotAName(t *testing.T) {
	if got := SafeDeviceName("phone\xff\xfe"); got != "" {
		t.Errorf("invalid UTF-8 was accepted as a name: %q", got)
	}
}
