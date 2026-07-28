// SPDX-License-Identifier: MIT

package notify

import (
	"strings"
	"testing"
)

// THE INCIDENT THIS ENCODES. A user typed `nfty.sh` for `ntfy.sh`; the panel
// said only "a topic is saved", so the transposition was invisible, and the
// address delivered — something answered at the typo host with a 2xx and a real
// alert left the house. Showing the host is what makes that mistake findable.
func TestRedactedEndpointShowsTheHostSoATypoIsVisible(t *testing.T) {
	got := RedactEndpoint("https://nfty.sh/phils_alerts")
	if !strings.Contains(got, "nfty.sh") {
		t.Fatalf("the host is hidden, so a typo stays invisible: %q", got)
	}
	if strings.Contains(got, "phils_alerts") {
		t.Errorf("the topic leaked to the browser: %q", got)
	}
}

// And the other half: the secret must not survive, whatever shape it arrives
// in. An ntfy topic IS its own access control and a Slack path is a right to
// post, so neither may ever reach the page.
func TestRedactedEndpointNeverLeaksTheSecretPart(t *testing.T) {
	cases := []struct {
		in     string
		secret string
	}{
		{"https://ntfy.sh/phils_alerts", "phils_alerts"},
		{"https://hooks.slack.com/services/T00/B11/xoxb-very-secret", "xoxb-very-secret"},
		{"https://example.com/ntfy/alerts", "alerts"},
		{"https://ntfy.example.com/t?auth=tk_secret", "tk_secret"},
		// Credentials in the address: the username is half of a basic-auth
		// guess, so it goes too rather than being bulleted.
		{"https://phil:hunter2@ntfy.example.com/topic", "hunter2"},
		{"https://phil:hunter2@ntfy.example.com/topic", "phil"},
	}
	for _, c := range cases {
		got := RedactEndpoint(c.in)
		if strings.Contains(got, c.secret) {
			t.Errorf("RedactEndpoint(%q) leaked %q: %q", c.in, c.secret, got)
		}
		if !strings.Contains(got, "•") {
			t.Errorf("RedactEndpoint(%q) = %q — nothing marks the hidden part", c.in, got)
		}
	}
}

// The bullet run is a fixed width. A bullet per character would publish the
// length of the secret, which is a free hint to anyone guessing it.
func TestRedactedEndpointDoesNotPublishTheSecretsLength(t *testing.T) {
	short := RedactEndpoint("https://ntfy.sh/ab")
	long := RedactEndpoint("https://ntfy.sh/a-very-much-longer-topic-name-indeed")
	if short != long {
		t.Errorf("the redaction reveals length: %q vs %q", short, long)
	}
}

func TestRedactedEndpointHandlesNothingSaved(t *testing.T) {
	for _, in := range []string{"", "   ", "not a url", "::::"} {
		if got := RedactEndpoint(in); got != "" {
			t.Errorf("RedactEndpoint(%q) = %q, want \"\" so the panel says nothing is saved", in, got)
		}
	}
}
