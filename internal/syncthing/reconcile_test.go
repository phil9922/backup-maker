// SPDX-License-Identifier: MIT

package syncthing

import "testing"

// TestUnderRootStopsAtASeparator pins the boundary that keeps EnforceReceiveOnly
// off folders this machine sends. A sibling directory sharing the root's name is
// the case a plain string prefix gets wrong, and being wrong there forces one of
// the user's own backups receive-only.
func TestUnderRootStopsAtASeparator(t *testing.T) {
	const root = "/mnt/backups"

	cases := []struct {
		name string
		path string
		want bool
	}{
		{"the root itself", "/mnt/backups", true},
		{"a received backup inside it", "/mnt/backups/my-laptop/code", true},
		{"an unclean path inside it", "/mnt/backups/./my-laptop/../my-laptop", true},
		{"a trailing slash on the root", "/mnt/backups/", true},

		{"a sibling starting with the root's name", "/mnt/backups-old", false},
		{"a sibling with content under it", "/mnt/backups-old/code", false},
		{"a sibling differing by one character", "/mnt/backups2", false},
		{"an unrelated path", "/home/pk/code", false},
		{"the parent", "/mnt", false},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := UnderRoot(c.path, root); got != c.want {
				t.Errorf("UnderRoot(%q, %q) = %v, want %v", c.path, root, got, c.want)
			}
		})
	}
}
