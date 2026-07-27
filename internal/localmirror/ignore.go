// SPDX-License-Identifier: MIT

package localmirror

import (
	"path/filepath"
	"strings"

	"github.com/phil9922/backup-maker/internal/config"
)

// Matcher implements backup-maker's simple ignore patterns for the local
// mirror engine (syncthing applies the same patterns on its own folders).
//   - "node_modules", "*.pyc": matched against every path segment
//   - "target/debug": matched as a sub-path anywhere in the tree
//   - anchored: exact paths from the folder root down, never name patterns
type Matcher struct {
	segment  []string // patterns without '/'
	subpath  []string // patterns with '/'
	anchored []string // paths relative to the folder root, matched literally
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

// NewMatcherFor builds a matcher for a walk of the folder rooted at root: the
// caller's patterns, plus the one exclusion that is not the caller's to choose
// — backup-maker's own configuration directory, whenever it lies inside root
// (see config.SelfExcludes). Every walk of a source folder goes through this
// constructor; NewMatcher applies patterns and nothing else.
func NewMatcherFor(root string, patterns []string) *Matcher {
	m := NewMatcher(patterns)
	m.anchored = config.SelfExcludes(root)
	return m
}

// Ignored reports whether relPath (slash-separated, relative to the folder
// root) matches any pattern.
func (m *Matcher) Ignored(relPath string) bool {
	relPath = filepath.ToSlash(relPath)
	// Anchored exclusions first, and compared literally against the path from
	// the folder root down: they are safety rules no pattern list can undo, and
	// matching them by name would take out unrelated directories that share it.
	for _, p := range m.anchored {
		if p == "" || relPath == p || strings.HasPrefix(relPath, p+"/") {
			return true
		}
	}
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
