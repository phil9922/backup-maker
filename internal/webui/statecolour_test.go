// SPDX-License-Identifier: MIT

package webui

import (
	"regexp"
	"strings"
	"testing"
)

// The dashboard puts the state word in a <span> inside its cell and hangs the
// state class on that span. The stylesheet has to be written to match it there.
//
// This is guarded because it has already gone wrong once, silently: the cards-
// on-a-phone layout moved the word into a span and left the stylesheet matching
// only `td.ok`, so every state in the backup table rendered in the body colour.
// Nothing errored, nothing logged, and the table still said the right words —
// which is exactly why a test has to be the thing that notices.
//
// The stake is not green. It is red: with no colour, an offline, stale or full
// destination is drawn identically to one holding a good backup, and this table
// exists to tell those apart at a glance.
func TestStateColoursReachTheWordTheyDescribe(t *testing.T) {
	css := readAsset(t, "static/style.css")
	js := readAsset(t, "static/app.js")

	// The classes app.js actually assigns, and where the stylesheet must be able
	// to reach them. A class is only listed here once something puts it on an
	// element; the point of the test is that the two files agree.
	for _, c := range []struct {
		class string
		// selectors that must all appear, one per place the class is used
		selectors []string
	}{
		{"ok", []string{"td .ok", ".card-meta .ok"}},
		{"busy", []string{"td .busy", ".card-meta .busy"}},
		{"bad", []string{"td .bad", ".card-meta .bad"}},
		{"paused", []string{"#archive-rows td .paused"}},
	} {
		if !strings.Contains(js, "'"+c.class+"'") {
			t.Errorf("app.js no longer assigns the %q class; if a state class was "+
				"renamed, rename it in style.css and here too", c.class)
		}
		for _, sel := range c.selectors {
			if !cssHasSelector(css, sel) {
				t.Errorf("style.css has no %q rule: the %q state is put on a span "+
					"inside its cell, so a selector that only matches the cell "+
					"itself leaves the state uncoloured", sel, c.class)
			}
		}
	}
}

// The state dot takes its colour from the text it sits beside. If that ever
// stops being true, colouring the word is no longer enough to colour the dot,
// and the rules above would pass while half the signal stayed grey.
func TestTheStateDotFollowsTheStateColour(t *testing.T) {
	css := readAsset(t, "static/style.css")
	dot := cssBlock(css, ".dot::before")
	if dot == "" {
		t.Fatal("no .dot::before rule in style.css")
	}
	if !strings.Contains(dot, "currentColor") {
		t.Errorf(".dot::before no longer uses currentColor for its background, so "+
			"it no longer follows the state colour beside it:\n%s", dot)
	}
}

func readAsset(t *testing.T, name string) string {
	t.Helper()
	b, err := staticFS.ReadFile(name)
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	return string(b)
}

// cssHasSelector reports whether sel appears as a selector in its own right,
// rather than as a substring of a longer one — `td .ok` must not be satisfied
// by `td .okay`, and must still be found in a comma-separated list.
func cssHasSelector(css, sel string) bool {
	q := regexp.QuoteMeta(sel)
	// Preceded by the start, a comma or a newline; followed by a comma or the
	// opening brace. Whitespace inside the selector is matched flexibly so
	// reformatting the stylesheet does not fail the test.
	q = strings.ReplaceAll(q, `\ `, `\s+`)
	return regexp.MustCompile(`(?m)(^|[,\n])\s*` + q + `\s*[,{]`).MatchString(css)
}

// cssBlock returns the declarations of the first rule whose selector list
// contains sel, or "" if there is none.
func cssBlock(css, sel string) string {
	i := strings.Index(css, sel)
	if i < 0 {
		return ""
	}
	open := strings.Index(css[i:], "{")
	end := strings.Index(css[i:], "}")
	if open < 0 || end < open {
		return ""
	}
	return css[i+open+1 : i+end]
}
