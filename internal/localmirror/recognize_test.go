// SPDX-License-Identifier: MIT

package localmirror

import (
	"os"
	"path/filepath"
	"testing"
)

// Recognize is the one rule every writer follows, so its answers are pinned
// here rather than left implicit in the engine's behavior.
func TestRecognizeClassifiesTheStorageAtATargetLocation(t *testing.T) {
	ours := t.TempDir()
	if err := WriteMarkerAt(ours, "uuid-1", "workstation"); err != nil {
		t.Fatal(err)
	}
	stranger := t.TempDir()
	if err := WriteMarkerAt(stranger, "somebody-elses-uuid", "their-laptop"); err != nil {
		t.Fatal(err)
	}
	reformatted := t.TempDir() // mounted, readable, and now blank
	gone := filepath.Join(t.TempDir(), "not-mounted")

	cases := []struct {
		name string
		root string
		want Recognition
	}{
		{"our own marker", ours, Ours},
		{"a different drive at the same place", stranger, Foreign},
		{"our drive reformatted", reformatted, Foreign},
		{"nothing mounted there", gone, Offline},
	}
	for _, c := range cases {
		if got := Recognize(NewLocalFS(c.root), "uuid-1"); got != c.want {
			t.Errorf("%s: Recognize = %v, want %v", c.name, got, c.want)
		}
	}
}

// The scenario reported from real hardware: the destination card was
// reformatted, so its marker went with the filesystem. The mirror must put
// nothing at all on it — not a directory, not a probe file, nothing.
func TestEngineWritesNothingToStorageWithoutOurMarker(t *testing.T) {
	src := t.TempDir()
	dst := t.TempDir() // the freshly reformatted card: mounted, empty
	mustWrite(t, filepath.Join(src, "notes.txt"), "private")

	e := New(Options{
		FolderID: "f1", TargetName: "card", SourcePath: src,
		Backend: NewLocalFS(dst), MachineName: "workstation", Label: "docs",
		UUID: "uuid-1", MaxAgeDays: 30, Log: quietLog(),
	})
	e.sync()

	if st := e.Status(); st.State != "offline" {
		t.Errorf("state = %q, want offline", st.State)
	}
	entries, err := os.ReadDir(dst)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Errorf("the engine put %d entries on unrecognised storage: %v", len(entries), names(entries))
	}
}

func names(entries []os.DirEntry) []string {
	out := make([]string, len(entries))
	for i, e := range entries {
		out[i] = e.Name()
	}
	return out
}
