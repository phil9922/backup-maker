// SPDX-License-Identifier: MIT

package notify

import (
	"net/url"
	"strings"
)

// bulletsFor is how much of a redacted tail to draw. Fixed rather than
// proportional to the real length: a bullet per character would publish the
// length of the secret, which is a free hint to anyone guessing it.
const bulletsFor = "••••••••"

// RedactEndpoint renders a stored delivery address so a panel can prove WHICH
// one is saved without disclosing the part that is a credential.
//
// WHY THIS EXISTS, AND THE INCIDENT THAT CAUSED IT. The dashboard was told only
// that "a topic is saved" — never which. A user typed `nfty.sh` instead of
// `ntfy.sh`, the transposition was invisible in a panel that echoed nothing
// back, and the address saved and *delivered*: something answered at the
// typo-squatted host and returned 2xx, so a real alert carrying the machine
// name and a destination name went to a stranger. Nothing in the product could
// have shown them, because the one screen that knew was refusing to say.
//
// So: the HOST is shown and everything after it is not. That split is the whole
// design. The host is what a person checks a typo against, and it is not the
// secret; the path is the secret — an ntfy topic name is its own access
// control, and a Slack webhook's path is a right to post. Showing
// `https://hooks.slack.com/••••••••` tells the owner which service they wired
// up and tells a shoulder-surfer nothing they can use.
//
// An unparseable or empty address returns "", which callers render as "nothing
// saved" rather than as a broken value.
func RedactEndpoint(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return ""
	}
	scheme := u.Scheme
	if scheme == "" {
		scheme = "https"
	}
	// Credentials in the address are dropped entirely rather than bulleted:
	// showing `user:••••@host` would disclose the username, which is half of
	// what a basic-auth guess needs.
	out := scheme + "://" + u.Host + "/"
	if strings.Trim(u.Path, "/") != "" || u.RawQuery != "" {
		out += bulletsFor
	}
	return out
}
