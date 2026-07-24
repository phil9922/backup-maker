// SPDX-License-Identifier: MIT

package localmirror

import "testing"

func TestMatcher(t *testing.T) {
	m := NewMatcher([]string{"node_modules", "*.pyc", "target/debug", ".cache"})
	cases := []struct {
		path string
		want bool
	}{
		{"src/main.go", false},
		{"node_modules/left-pad/index.js", true},
		{"deep/node_modules/x.js", true},
		{"lib/util.pyc", true},
		{"lib/util.py", false},
		{"target/debug/app", true},
		{"target/release/app", false},
		{"crates/foo/target/debug/x", true},
		{".git/HEAD", false},
		{".cache/x", true},
		{"my.cache.file", false},
	}
	for _, c := range cases {
		if got := m.Ignored(c.path); got != c.want {
			t.Errorf("Ignored(%q) = %v, want %v", c.path, got, c.want)
		}
	}
}
