// SPDX-License-Identifier: MIT

// Package update notices when a newer backup-maker release exists and says so.
//
// IT ONLY EVER TELLS YOU. Nothing here downloads a binary, replaces one, or
// runs anything — the whole package is a version string, a comparison, and a
// message. That is a deliberate ceiling, not a first step: an updater that
// installs by itself can break every machine's backups at once, and silent
// failure is the one outcome this program exists to prevent.
//
// AND IT IS OFF UNLESS SWITCHED ON. Checking contacts github.com, and this
// project promises that nothing reaches the internet unless the user asked for
// it. A fresh install checks nothing.
package update

import (
	"strconv"
	"strings"
)

// Newer reports whether latest is a later release than current.
//
// FALSE WHENEVER IT CANNOT BE SURE. A wrong "no update" is a missed release; a
// wrong "update available" is a notification pointing at nothing, sent to
// someone who then goes looking. On a build with no release version at all —
// anything built from source — that means staying quiet rather than telling a
// developer their own working tree is out of date.
func Newer(current, latest string) bool {
	c, ok := parse(current)
	if !ok {
		return false
	}
	l, ok := parse(latest)
	if !ok {
		return false
	}
	for i := range 3 {
		if l[i] != c[i] {
			return l[i] > c[i]
		}
	}
	return false
}

// Comparable reports whether a version string is a real release, and so can be
// compared with one.
//
// Used to decide whether to ask github.com at all: a build from source calls
// itself "dev", which can never be newer or older than a release, so asking
// would be a network request whose answer is discarded — on every contributor's
// machine, for ever.
func Comparable(v string) bool {
	_, ok := parse(v)
	return ok
}

// parse reads "v0.1.6", "0.1.6" or "0.1.6-next" into major/minor/patch.
//
// A PRE-RELEASE SUFFIX IS REFUSED RATHER THAN IGNORED. "0.1.7-next" is what a
// snapshot build calls itself, and it is NOT release 0.1.7 — treating it as one
// would have every developer snapshot announce a release that does not exist,
// and would silence the real 0.1.7 when it arrived.
func parse(v string) ([3]int, bool) {
	var out [3]int
	v = strings.TrimSpace(v)
	v = strings.TrimPrefix(v, "v")
	if v == "" || strings.ContainsAny(v, "-+") {
		return out, false
	}
	parts := strings.Split(v, ".")
	if len(parts) != 3 {
		return out, false
	}
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 {
			return out, false
		}
		out[i] = n
	}
	return out, true
}
