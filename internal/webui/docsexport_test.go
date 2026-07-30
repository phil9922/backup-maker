// SPDX-License-Identifier: MIT

package webui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
)

func exportTo(t *testing.T) (string, *ExportedDocs) {
	t.Helper()
	withDocs(t)
	dir := filepath.Join(t.TempDir(), "manual")
	out, err := ExportDocs(dir)
	if err != nil {
		t.Fatalf("ExportDocs: %v", err)
	}
	return dir, out
}

func read(t *testing.T, parts ...string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(parts...))
	if err != nil {
		t.Fatalf("reading the export: %v", err)
	}
	return string(raw)
}

// THE POINT OF THE FEATURE: a directory somebody can open with no daemon, no
// web server and no internet. Every one of those is a way it could fail while
// still producing files.
func TestAnExportOpensWithNoDaemonAndNoNetwork(t *testing.T) {
	dir, out := exportTo(t)

	// index.html, because that is what a browser opens for a directory.
	index := read(t, dir, "index.html")
	if !strings.Contains(index, "<h1") {
		t.Error("the index is not rendered HTML")
	}
	if out.Pages < 4 {
		t.Errorf("wrote %d pages, want every page in the tree", out.Pages)
	}

	// No absolute paths: over file:// a leading / is the root of the disk.
	for _, page := range []string{"index.html", filepath.Join("guide", "1-install.html")} {
		body := read(t, dir, page)
		for _, bad := range []string{`href="/`, `src="/`} {
			if strings.Contains(body, bad) {
				t.Errorf("%s contains an absolute link (%s), which breaks over file://", page, bad)
			}
		}
		for _, external := range []string{"cdn.", "googleapis", "unpkg", "jsdelivr"} {
			if strings.Contains(body, external) {
				t.Errorf("%s pulls from %q — it would not render offline", page, external)
			}
		}
	}

	// The stylesheet travels with it, and is reachable from a nested page.
	if _, err := os.Stat(filepath.Join(dir, "style.css")); err != nil {
		t.Errorf("the export has no stylesheet: %v", err)
	}
	if !strings.Contains(read(t, dir, "guide", "1-install.html"), `href="../style.css"`) {
		t.Error("a nested page does not reach the stylesheet")
	}
}

// A page that links to another must land on a FILE THAT EXISTS. This is the
// test that would have caught .md links being written into an export, which
// look right and download raw Markdown when clicked.
func TestEveryLinkInAnExportPointsAtAFileThatExists(t *testing.T) {
	dir, _ := exportTo(t)
	checked := 0
	err := filepath.Walk(dir, func(p string, fi os.FileInfo, err error) error {
		if err != nil || fi.IsDir() || !strings.HasSuffix(p, ".html") {
			return err
		}
		body, rerr := os.ReadFile(p)
		if rerr != nil {
			return rerr
		}
		for _, m := range docsHref.FindAllStringSubmatch(string(body), -1) {
			target := m[1]
			if target == "" || strings.HasPrefix(target, "#") || strings.HasPrefix(target, "http") {
				continue
			}
			if i := strings.Index(target, "#"); i >= 0 {
				target = target[:i]
			}
			checked++
			at := filepath.Join(filepath.Dir(p), filepath.FromSlash(target))
			if _, serr := os.Stat(at); serr != nil {
				t.Errorf("%s links to %q, which is not in the export", p, target)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if checked == 0 {
		t.Fatal("no links were checked; the walk proved nothing")
	}
}

// Images are the reason the export is worth having rather than the Markdown —
// and the thing a naive "render the pages" would forget to copy.
func TestAnExportCarriesItsImages(t *testing.T) {
	dir, _ := exportTo(t)
	if !strings.Contains(read(t, dir, "guide", "2-first.html"), `src="../screenshots/x.png"`) {
		t.Error("an image link was not made relative")
	}
	raw, err := os.ReadFile(filepath.Join(dir, "screenshots", "x.png"))
	if err != nil {
		t.Fatalf("the image itself was not copied: %v", err)
	}
	if !strings.HasPrefix(string(raw), "\x89PNG") {
		t.Error("the image bytes were mangled")
	}
}

// AN EXPORT MUST NOT SCATTER FILES THROUGH SOMEBODY'S WORK. `--export .` in the
// wrong terminal is one keystroke away, and the files it writes have ordinary
// names. Nothing is deleted either way; this is about refusing to write.
func TestAnExportRefusesADirectoryThatIsNotItsOwn(t *testing.T) {
	withDocs(t)
	dir := t.TempDir()
	keep := filepath.Join(dir, "thesis.txt")
	if err := os.WriteFile(keep, []byte("years of work"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := ExportDocs(dir); err == nil {
		t.Fatal("exported into a directory holding somebody else's files")
	}
	if got := read(t, keep); got != "years of work" {
		t.Errorf("the existing file was touched: %q", got)
	}
	if _, err := os.Stat(filepath.Join(dir, "index.html")); err == nil {
		t.Error("a page was written into the refused directory")
	}
}

// ...but exporting to the same place twice is the ordinary thing, so the
// refusal must recognise its own output.
func TestExportingTwiceToTheSamePlaceWorks(t *testing.T) {
	dir, _ := exportTo(t)
	if _, err := ExportDocs(dir); err != nil {
		t.Fatalf("re-exporting over a previous export was refused: %v", err)
	}
	if !strings.Contains(read(t, dir, "index.html"), "<h1") {
		t.Error("the second export did not write the pages")
	}
}

// An export that failed halfway must be retryable. It is marked on the way in
// for exactly this reason.
func TestARetryAfterAHalfWrittenExportIsNotRefused(t *testing.T) {
	withDocs(t)
	dir := filepath.Join(t.TempDir(), "manual")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	// What a first attempt leaves behind when it dies after the marker.
	if err := os.WriteFile(filepath.Join(dir, exportMarker), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "index.html"), []byte("half"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := ExportDocs(dir); err != nil {
		t.Fatalf("a retry was refused: %v", err)
	}
}

func TestExportingWithNoDocumentationSaysSo(t *testing.T) {
	prev := docsRoot
	docsRoot = nil
	t.Cleanup(func() { docsRoot = prev })
	if _, err := ExportDocs(filepath.Join(t.TempDir(), "manual")); err == nil {
		t.Fatal("expected an error when the build carries no documentation")
	}
}

// THE GUARANTEE: the fingerprint changes when the manual does.
//
// It is what decides whether a copy sitting on a backup destination is replaced,
// and the failure it prevents is invisible: a manual describing an older version
// of the program looks exactly as authoritative as a current one, to a reader who
// has no way of checking it against anything. Keying on the version string alone
// would miss every change made before the next release.
func TestTheManualsFingerprintChangesWhenTheDocumentationDoes(t *testing.T) {
	withDocs(t)
	was, err := DocsFingerprint()
	if err != nil {
		t.Fatal(err)
	}
	if again, err := DocsFingerprint(); err != nil || again != was {
		t.Fatalf("the same documentation fingerprinted differently twice (%q then %q, err %v): "+
			"every daemon would rewrite the manual on every recheck", was, again, err)
	}

	for _, c := range []struct {
		what string
		docs fstest.MapFS
	}{
		{"a page's words changed", fstest.MapFS{
			"docs/README.md":          {Data: []byte("# Start\n\nSomething else entirely.\n")},
			"docs/guide/1-install.md": {Data: []byte("# 1. Installing\n")},
		}},
		{"a page was added", fstest.MapFS{
			"docs/README.md":          {Data: []byte("# Start\n")},
			"docs/guide/1-install.md": {Data: []byte("# 1. Installing\n")},
			"docs/guide/2-new.md":     {Data: []byte("# 2. New\n")},
		}},
		{"a page was renamed", fstest.MapFS{
			"docs/README.md":          {Data: []byte("# Start\n")},
			"docs/guide/1-getting.md": {Data: []byte("# 1. Installing\n")},
		}},
		{"a screenshot was replaced", fstest.MapFS{
			"docs/README.md":          {Data: []byte("# Start\n")},
			"docs/guide/1-install.md": {Data: []byte("# 1. Installing\n")},
			"docs/screenshots/x.png":  {Data: []byte("\x89PNG\r\n\x1a\ndifferent")},
		}},
	} {
		base := fstest.MapFS{
			"docs/README.md":          {Data: []byte("# Start\n")},
			"docs/guide/1-install.md": {Data: []byte("# 1. Installing\n")},
			"docs/screenshots/x.png":  {Data: []byte("\x89PNG\r\n\x1a\n")},
		}
		SetDocs(base)
		before, err := DocsFingerprint()
		if err != nil {
			t.Fatal(err)
		}
		SetDocs(c.docs)
		after, err := DocsFingerprint()
		if err != nil {
			t.Fatal(err)
		}
		if before == after {
			t.Errorf("%s and the fingerprint did not: a destination would keep the old manual", c.what)
		}
	}
}

// A build carrying no documentation has no fingerprint, rather than a fingerprint
// of nothing — which would be a value a destination could match and so keep an
// old manual for ever.
func TestNoDocumentationHasNoFingerprint(t *testing.T) {
	prev := docsRoot
	t.Cleanup(func() { docsRoot = prev })
	docsRoot = nil
	if id, err := DocsFingerprint(); err == nil {
		t.Errorf("a build with no documentation fingerprinted as %q", id)
	}
}
