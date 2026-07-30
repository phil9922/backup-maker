// SPDX-License-Identifier: MIT

package webui

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
)

// withDocs installs a small documentation tree for the duration of a test.
func withDocs(t *testing.T) {
	t.Helper()
	prev := docsRoot
	t.Cleanup(func() { docsRoot = prev })
	SetDocs(fstest.MapFS{
		"docs/README.md":             {Data: []byte("# Start\n\nSee [install](guide/1-install.md).\n")},
		"docs/guide/1-install.md":    {Data: []byte("# 1. Installing\n\nBack to [the index](../README.md).\n\n| a | b |\n|---|---|\n| 1 | 2 |\n")},
		"docs/guide/2-first.md":      {Data: []byte("# 2. First backup\n\n![shot](../screenshots/x.png)\n")},
		"docs/setup/hardware.md":     {Data: []byte("# Hardware\n")},
		"docs/reference/security.md": {Data: []byte("# Security\n")},
		"docs/screenshots/x.png":     {Data: []byte("\x89PNG\r\n\x1a\n")},
	})
}

func getDocs(t *testing.T, path string) *httptest.ResponseRecorder {
	t.Helper()
	s := &Server{}
	rec := httptest.NewRecorder()
	s.handleDocs(rec, httptest.NewRequest(http.MethodGet, path, nil))
	return rec
}

func TestDocsIndexRenders(t *testing.T) {
	withDocs(t)
	rec := getDocs(t, "/docs")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /docs = %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "<h1") {
		t.Error("the index did not render as HTML")
	}
}

// THE POINT OF THE WHOLE FEATURE: the manual is readable on a machine with no
// route to the internet, so the pages must come from the binary and contain no
// request to anywhere else.
func TestDocsAreServedFromTheBinaryWithNoExternalRequests(t *testing.T) {
	withDocs(t)
	body := getDocs(t, "/docs/guide/1-install.md").Body.String()
	for _, external := range []string{"cdn.", "googleapis", "unpkg", "jsdelivr"} {
		if strings.Contains(body, external) {
			t.Errorf("the rendered page pulls from %q — it would break offline", external)
		}
	}
	if !strings.Contains(body, "<table") {
		t.Error("tables did not render; the documentation uses them")
	}
}

// Links between pages are written relative so they work on GitHub. Served over
// HTTP they have to be rewritten, or every cross-reference 404s.
func TestRelativeLinksAreRewrittenToTheDocsRoute(t *testing.T) {
	withDocs(t)
	body := getDocs(t, "/docs/guide/1-install.md").Body.String()
	if !strings.Contains(body, `href="/docs/README.md"`) {
		t.Errorf("a ../ link was not resolved to the docs route:\n%s", body)
	}
	if strings.Contains(body, `href="../README.md"`) {
		t.Error("a raw relative link survived; it would 404 when clicked")
	}
}

// Images live alongside the pages and must resolve the same way.
func TestImagesAreRewrittenAndServed(t *testing.T) {
	withDocs(t)
	body := getDocs(t, "/docs/guide/2-first.md").Body.String()
	if !strings.Contains(body, `src="/docs/screenshots/x.png"`) {
		t.Errorf("image path was not rewritten:\n%s", body)
	}
	rec := getDocs(t, "/docs/screenshots/x.png")
	if rec.Code != http.StatusOK {
		t.Fatalf("serving an embedded image returned %d", rec.Code)
	}
	if !strings.HasPrefix(rec.Body.String(), "\x89PNG") {
		t.Error("the image bytes were not served intact")
	}
}

// The sidebar is built by reading the tree, which is why the guide filenames are
// numbered: sorted order IS reading order. A page added later must appear in the
// right place with no code change.
func TestTheSidebarIsBuiltFromTheTreeInReadingOrder(t *testing.T) {
	withDocs(t)
	body := getDocs(t, "/docs").Body.String()
	first := strings.Index(body, "1. Installing")
	second := strings.Index(body, "2. First backup")
	if first < 0 || second < 0 {
		t.Fatalf("guide pages missing from the sidebar:\n%s", body)
	}
	if first > second {
		t.Error("the sidebar is not in reading order")
	}
	for _, section := range []string{"The guide", "Setting up hardware", "Reference"} {
		if !strings.Contains(body, section) {
			t.Errorf("the sidebar is missing the %q section", section)
		}
	}
}

// A dozen cross-references in the tree point at a heading rather than the top
// of a page. rewriteDocsLinks keeps the fragment; this is the other half —
// without auto heading IDs goldmark emits no id at all, so every one of those
// links silently lands at the top here while working correctly on GitHub.
func TestHeadingsCarryAnchorsSoCrossReferencesLand(t *testing.T) {
	prev := docsRoot
	t.Cleanup(func() { docsRoot = prev })
	SetDocs(fstest.MapFS{
		"docs/guide/1-install.md": {Data: []byte(
			"# 1. Installing\n\nSee [later](2-first.md#adding-backup-targets).\n\n## Adding backup targets\n")},
	})
	body := getDocs(t, "/docs/guide/1-install.md").Body.String()
	if !strings.Contains(body, `id="adding-backup-targets"`) {
		t.Errorf("heading carries no anchor, so #adding-backup-targets goes nowhere:\n%s", body)
	}
	if !strings.Contains(body, `href="/docs/guide/2-first.md#adding-backup-targets"`) {
		t.Errorf("the fragment did not survive link rewriting:\n%s", body)
	}
}

// The tree is embedded and read-only, but a request must still not be able to
// name its way out of it.
func TestDocsRefuseTraversalAndUnknownPages(t *testing.T) {
	withDocs(t)
	for _, p := range []string{
		"/docs/../../etc/passwd",
		"/docs/nope.md",
		"/docs/guide/../../secret.md",
	} {
		if code := getDocs(t, p).Code; code != http.StatusNotFound {
			t.Errorf("GET %s = %d, want 404", p, code)
		}
	}
}

// THE FIRST LINK ON THE DOCUMENTATION INDEX is "read the guide", written
// `guide/` so it works on GitHub. It 404'd here for as long as this viewer has
// existed: a directory is neither Markdown nor an image, so the handler
// appended .md and looked for `guide.md`. A folder means its first page in
// reading order.
func TestALinkToAFolderOpensItsFirstPage(t *testing.T) {
	withDocs(t)
	for _, p := range []string{"/docs/guide/", "/docs/guide"} {
		rec := getDocs(t, p)
		if rec.Code != http.StatusOK {
			t.Fatalf("GET %s = %d, want the guide's first page", p, rec.Code)
		}
		if !strings.Contains(rec.Body.String(), "1. Installing") {
			t.Errorf("GET %s did not open the first page of the guide", p)
		}
	}
}

// And the link itself has to be readdressed, or it never reaches the handler
// above in a form that works.
func TestAFolderLinkIsRewrittenToAPage(t *testing.T) {
	prev := docsRoot
	t.Cleanup(func() { docsRoot = prev })
	SetDocs(fstest.MapFS{
		"docs/README.md":          {Data: []byte("# Start\n\nRead [the guide](guide/).\n")},
		"docs/guide/1-install.md": {Data: []byte("# 1. Installing\n")},
	})
	body := getDocs(t, "/docs").Body.String()
	if !strings.Contains(body, `href="/docs/guide/1-install.md"`) {
		t.Errorf("the folder link was not resolved to a page:\n%s", body)
	}
}

// A build that somehow carries no documentation says so rather than panicking.
func TestDocsAbsentIsHandled(t *testing.T) {
	prev := docsRoot
	docsRoot = nil
	t.Cleanup(func() { docsRoot = prev })
	if code := getDocs(t, "/docs").Code; code != http.StatusNotFound {
		t.Errorf("with no docs embedded, GET /docs = %d, want 404", code)
	}
}
