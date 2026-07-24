// SPDX-License-Identifier: MIT

package localmirror

import (
	"path/filepath"
	"strings"
)

// Matcher implements backup-maker's simple ignore patterns for the local
// mirror engine (syncthing applies the same patterns on its own folders).
//   - "node_modules", "*.pyc": matched against every path segment
//   - "target/debug": matched as a sub-path anywhere in the tree
type Matcher struct {
	segment []string // patterns without '/'
	subpath []string // patterns with '/'
}

func NewMatcher(patterns []string) *Matcher {
	m := &Matcher{}
	for _, p := range patterns {
		p = strings.Trim(strings.TrimSpace(filepath.ToSlash(p)), "/")
		if p == "" {
			continue
		}
		if strings.Contains(p, "/") {
			m.subpath = append(m.subpath, p)
		} else {
			m.segment = append(m.segment, p)
		}
	}
	return m
}

// Ignored reports whether relPath (slash-separated, relative to the folder
// root) matches any pattern.
func (m *Matcher) Ignored(relPath string) bool {
	relPath = filepath.ToSlash(relPath)
	for _, seg := range strings.Split(relPath, "/") {
		for _, p := range m.segment {
			if ok, _ := filepath.Match(p, seg); ok {
				return true
			}
		}
	}
	wrapped := "/" + relPath + "/"
	for _, p := range m.subpath {
		if strings.Contains(wrapped, "/"+p+"/") {
			return true
		}
	}
	return false
}
