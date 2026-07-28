// SPDX-License-Identifier: MIT

package setup

import (
	"fmt"
	"strings"
)

// NormalizeDeviceID tidies a device ID pasted from another machine and checks
// that it could be one.
//
// It lives here rather than in the CLI so the command line and the dashboard
// cannot drift into accepting different things — a rule enforced in only one of
// them is a rule that will eventually disagree with itself.
//
// The check is deliberately loose: syncthing owns the real format (including
// its check digits), and rejecting an ID it would have accepted leaves the user
// with no way in at all. Length and group count catch what actually happens —
// a half-copied ID, or something pasted from the wrong place.
func NormalizeDeviceID(raw string) (string, error) {
	id := strings.ToUpper(strings.TrimSpace(raw))
	if len(id) < 50 || strings.Count(id, "-") < 7 {
		return "", fmt.Errorf("that doesn't look like a device ID (expected XXXXXXX-XXXXXXX-... from \"backup-maker pair\" on the other machine)")
	}
	return id, nil
}
