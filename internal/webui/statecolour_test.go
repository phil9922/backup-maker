// SPDX-License-Identifier: MIT

package webui

import (
	"regexp"
	"strings"
	"testing"

	"github.com/phil9922/backup-maker/internal/status"
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
		{"ok", []string{"td .ok", ".card-meta .ok", ".alert-head .ok"}},
		{"busy", []string{"td .busy", ".card-meta .busy"}},
		{"bad", []string{"td .bad", ".card-meta .bad", ".alert-head .bad"}},
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

// THE GUARANTEE: the browser says the same words the Go surfaces say.
//
// internal/status/vocabulary.go is the one copy for the CLI and for the page
// written onto each destination, but the dashboard is JavaScript and cannot share
// it. This is the seam where the vocabulary can still drift — and it has, twice:
// "scheduled" versus "timed", and then "in sync" versus "backed up", which
// survived two releases on the status page.
//
// So the words are asserted from the Go side against the file that renders them.
// If a state is renamed, this fails until app.js is brought along.
func TestEveryStateHasTheSameWordsInTheBrowser(t *testing.T) {
	js := readAsset(t, "static/app.js")
	// Every distinct word the Go vocabulary can produce for a healthy or
	// in-progress state. Faults keep their engine names on both sides.
	for _, phrase := range []string{
		status.RowLabel(status.Row{State: "in sync"}),                     // backed up
		status.RowLabel(status.Row{State: "scanning", FirstBackup: true}), // first backup
		status.ArchiveLabel(status.ArchiveRow{State: "never run"}),        // not backed up yet
	} {
		if !strings.Contains(js, phrase) {
			t.Errorf("app.js never says %q, but the CLI and the destination status "+
				"page do: the dashboard has drifted from internal/status/vocabulary.go",
				phrase)
		}
	}
	// And the state names it branches on must still be states the Go side emits,
	// or a rename leaves the browser matching a string nothing produces.
	for _, state := range []string{"in sync", "scanning", "syncing", "awaiting-pair"} {
		if !strings.Contains(js, "'"+state+"'") {
			t.Errorf("app.js no longer mentions the %q state; if it was renamed, "+
				"rename it in internal/status too", state)
		}
	}
}

// THE VERDICT MUST NOT KEEP ITS OWN IDEA OF WHAT IS BROKEN.
//
// It used to, and the list was wrong in both directions: it named
// 'unrecognised', which no Go code has ever produced, and it omitted
// 'wrong-drive' and 'name-clash' — the two states that mean backup-maker is
// REFUSING TO WRITE to a destination. So a reformatted card or another computer
// holding this machine's folder produced "Everything is backed up" as the
// headline, directly above a panel saying nothing was being written. The one
// line this page exists to get right was the line contradicting the rest of it.
//
// The fix is that it asks stateClass, the same mapping the tables and dots use,
// so a state added to the daemon is red in the headline the moment it is red
// anywhere. This test fails if that derivation is replaced by a literal list
// again.
func TestTheVerdictAsksTheSharedStateMappingInsteadOfItsOwnList(t *testing.T) {
	js := readAsset(t, "static/app.js")
	start := strings.Index(js, "function renderVerdict(")
	if start < 0 {
		t.Fatal("renderVerdict is gone; this guard needs rewriting to match")
	}
	end := strings.Index(js[start:], "\n}\n")
	if end < 0 {
		t.Fatal("could not find the end of renderVerdict")
	}
	body := js[start : start+end]

	if !strings.Contains(body, "stateClass(") {
		t.Error("renderVerdict no longer asks stateClass: it has gone back to " +
			"deciding for itself which states are broken, which is how " +
			"'wrong-drive' and 'name-clash' were read as \"Everything is backed up\"")
	}
	// Every state the shared mapping calls a fault, other than the one the
	// verdict handles separately. None may be named in here: naming one is the
	// beginning of a second list.
	for _, state := range []string{"stale", "full", "wrong-drive", "name-clash", "error"} {
		if strings.Contains(body, "'"+state+"'") {
			t.Errorf("renderVerdict names the %q state directly; it should be "+
				"deriving that from stateClass so a new fault is covered "+
				"automatically", state)
		}
	}
	// The phantom that started it. A state name nothing produces is dead code
	// that reads as coverage.
	if strings.Contains(js, "'unrecognised'") {
		t.Error("app.js branches on an 'unrecognised' state, which no Go code " +
			"emits — the real refusal states are 'wrong-drive' and 'name-clash'")
	}
}

// A REFUSAL MUST EXPLAIN ITSELF. These are the two states where backup-maker is
// deliberately writing nothing, and neither name says what to do about it. They
// now reach the screen within a minute of the storage changing rather than
// whenever the next pass runs, so a red word with no remedy beside it is what
// somebody would be left with.
func TestBothRefusalStatesTellYouWhatToDo(t *testing.T) {
	js := readAsset(t, "static/app.js")
	help := js[strings.Index(js, "function stateHelp("):]
	help = help[:strings.Index(help, "\n}\n")]
	for _, state := range []string{"wrong-drive", "name-clash"} {
		if !strings.Contains(help, "'"+state+"'") {
			t.Errorf("stateHelp says nothing about %q, so the dashboard shows a "+
				"red word for a destination it is refusing to write to and no "+
				"way to act on it", state)
		}
	}
}

// The browser's copy of the mapping must agree with Go's on the states that are
// NOT faults, or the dashboard reddens something the CLI and the destination
// status page call fine.
func TestTheBrowserAgreesOnWhatIsNotAFault(t *testing.T) {
	js := readAsset(t, "static/app.js")
	for _, state := range []string{"no destination yet", "no folders assigned"} {
		if status.RowHealth(status.Row{State: state}) != "muted" {
			t.Fatalf("this test assumes %q is muted in Go; it is not", state)
		}
		if !strings.Contains(js, "'"+state+"'") {
			t.Errorf("app.js does not special-case %q, so stateClass falls through "+
				"to 'bad' and the dashboard draws a destination red that Go calls "+
				"muted — and the verdict then counts it as needing attention", state)
		}
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
