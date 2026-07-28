// SPDX-License-Identifier: MIT

package webui

import (
	"bytes"
	"fmt"
	"html"
	"io/fs"
	"net/http"
	"path"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/extension"
	"github.com/yuin/goldmark/parser"
)

// docsFS is the embedded documentation tree, installed by the main package.
//
// A PACKAGE-LEVEL VALUE RATHER THAN A PARAMETER, for the same reason staticFS
// is one: it is a compile-time constant asset of the program, identical in
// every daemon, and threading it through Run → daemon → Server signatures would
// add three parameters that can only ever hold one value. It is written once at
// init and read thereafter.
var (
	docsMu   sync.RWMutex
	docsRoot fs.FS
)

// SetDocs installs the embedded documentation. Called from the main package,
// which is the only place that can hold the go:embed directive for a directory
// outside internal/.
func SetDocs(fsys fs.FS) {
	docsMu.Lock()
	defer docsMu.Unlock()
	sub, err := fs.Sub(fsys, "docs")
	if err != nil {
		return
	}
	docsRoot = sub
}

func docsAvailable() bool {
	docsMu.RLock()
	defer docsMu.RUnlock()
	return docsRoot != nil
}

func docsFiles() fs.FS {
	docsMu.RLock()
	defer docsMu.RUnlock()
	return docsRoot
}

// md renders CommonMark plus tables, which the documentation uses.
//
// WithAutoHeadingID is not optional here. Without it goldmark emits headings
// with no id attribute, so every `page.md#some-heading` cross-reference in the
// tree — and there are a dozen — silently lands at the top of the page in this
// viewer while working correctly on GitHub. rewriteDocsLinks goes to the
// trouble of preserving those fragments; this is what makes them mean
// something. goldmark's slug matches GitHub's for the headings used here.
var md = goldmark.New(
	goldmark.WithExtensions(extension.GFM),
	goldmark.WithParserOptions(parser.WithAutoHeadingID()),
)

// docsPage is one entry in the sidebar.
type docsPage struct {
	Path  string // "guide/1-install.md"
	Title string // from the page's own first heading
}

// docsSection groups pages under a heading in the sidebar.
type docsSection struct {
	Name  string
	Pages []docsPage
}

// docsNav builds the sidebar by READING THE TREE rather than from a hardcoded
// list.
//
// That is the whole reason the guide's filenames are numbered: sorted order is
// reading order, so adding `7-something.md` puts it in the right place in the
// navigation with no code change and no list to forget to update. A hardcoded
// nav would drift the first time somebody added a page.
func docsNav() []docsSection {
	fsys := docsFiles()
	if fsys == nil {
		return nil
	}
	out := []docsSection{}
	for _, dir := range []struct{ path, name string }{
		{"guide", "The guide"},
		{"setup", "Setting up hardware"},
		{"reference", "Reference"},
	} {
		entries, err := fs.ReadDir(fsys, dir.path)
		if err != nil {
			continue
		}
		var pages []docsPage
		for _, e := range entries {
			if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
				continue
			}
			p := dir.path + "/" + e.Name()
			pages = append(pages, docsPage{Path: p, Title: docsTitle(fsys, p)})
		}
		sort.Slice(pages, func(i, j int) bool { return pages[i].Path < pages[j].Path })
		if len(pages) > 0 {
			out = append(out, docsSection{Name: dir.name, Pages: pages})
		}
	}
	return out
}

// docsTitle reads a page's own first heading, so the sidebar cannot disagree
// with the page it points at.
func docsTitle(fsys fs.FS, p string) string {
	raw, err := fs.ReadFile(fsys, p)
	if err != nil {
		return path.Base(p)
	}
	for line := range strings.Lines(string(raw)) {
		if t, ok := strings.CutPrefix(strings.TrimSpace(line), "# "); ok {
			return strings.TrimSpace(t)
		}
	}
	return path.Base(p)
}

var (
	// Links between pages are written as ordinary relative Markdown links so
	// they work on GitHub; served over HTTP they have to point at this route
	// instead.
	docsMDLink = regexp.MustCompile(`href="([^"]+\.md)(#[^"]*)?"`)
	docsImg    = regexp.MustCompile(`src="([^"]+)"`)
)

// handleDocs serves the documentation as a navigable site.
//
// Loopback-only and read-only, like the rest of the dashboard. It is NOT added
// to the network view's allow-list: that listener serves a fixed set of health
// routes and nothing else, and while the docs disclose nothing about this
// household, widening that allow-list is not something to do for convenience.
func (s *Server) handleDocs(w http.ResponseWriter, r *http.Request) {
	if !docsAvailable() {
		http.Error(w, "this build carries no documentation", http.StatusNotFound)
		return
	}
	fsys := docsFiles()

	rel := strings.TrimPrefix(r.URL.Path, "/docs")
	rel = strings.Trim(rel, "/")
	if rel == "" {
		rel = "README.md"
	}
	// Serve images (and anything else non-Markdown) straight from the tree.
	if !strings.HasSuffix(rel, ".md") {
		if ext := path.Ext(rel); ext == ".png" || ext == ".jpg" || ext == ".jpeg" || ext == ".gif" || ext == ".svg" {
			serveDocsAsset(w, r, fsys, rel)
			return
		}
		rel += ".md"
	}
	// fs.ValidPath rejects anything with "..", an absolute path or a trailing
	// slash, which is the whole of the traversal defence — the tree is embedded
	// and read-only, but a request must still not be able to name its way out
	// of it.
	if !fs.ValidPath(rel) {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	raw, err := fs.ReadFile(fsys, rel)
	if err != nil {
		http.Error(w, "no such page", http.StatusNotFound)
		return
	}

	var body bytes.Buffer
	if err := md.Convert(raw, &body); err != nil {
		http.Error(w, "that page could not be rendered", http.StatusInternalServerError)
		return
	}
	rendered := rewriteDocsLinks(body.String(), path.Dir(rel))

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	fmt.Fprint(w, docsShell(rel, rendered))
}

// rewriteDocsLinks turns the relative links that work on GitHub into ones that
// work under /docs, resolving them against the page doing the linking.
func rewriteDocsLinks(htmlBody, dir string) string {
	htmlBody = docsMDLink.ReplaceAllStringFunc(htmlBody, func(m string) string {
		parts := docsMDLink.FindStringSubmatch(m)
		target, anchor := parts[1], parts[2]
		if strings.HasPrefix(target, "http") {
			return m
		}
		// The fragment has to survive: many cross-references in these pages
		// point at a specific heading, not the top of a page.
		return `href="/docs/` + path.Clean(path.Join(dir, target)) + anchor + `"`
	})
	htmlBody = docsImg.ReplaceAllStringFunc(htmlBody, func(m string) string {
		parts := docsImg.FindStringSubmatch(m)
		src := parts[1]
		if strings.HasPrefix(src, "http") || strings.HasPrefix(src, "/") {
			return m
		}
		return `src="/docs/` + path.Clean(path.Join(dir, src)) + `"`
	})
	return htmlBody
}

func serveDocsAsset(w http.ResponseWriter, r *http.Request, fsys fs.FS, rel string) {
	if !fs.ValidPath(rel) {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	raw, err := fs.ReadFile(fsys, rel)
	if err != nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	w.Header().Set("Cache-Control", "no-cache")
	// No modification time: an embedded file has none, and inventing one would
	// hand the browser a validator that means nothing. Cache-Control governs
	// reuse instead.
	http.ServeContent(w, r, path.Base(rel), time.Time{}, bytes.NewReader(raw))
}

// docsShell wraps a rendered page in the sidebar and the dashboard's own
// styling, so the documentation looks like part of the program rather than a
// bare Markdown dump.
func docsShell(current, body string) string {
	var b strings.Builder
	b.WriteString(`<!doctype html><html lang="en"><head><meta charset="utf-8">`)
	b.WriteString(`<meta name="viewport" content="width=device-width, initial-scale=1">`)
	b.WriteString(`<title>` + html.EscapeString(docsTitle(docsFiles(), current)) + ` — backup-maker docs</title>`)
	b.WriteString(`<link rel="stylesheet" href="/style.css"><link rel="icon" href="/favicon.ico">`)
	b.WriteString(`</head><body class="docs">`)
	// No explicit "back to the dashboard" link: Docs opens in its own tab, so
	// there is nothing to go back FROM — following one would just leave two
	// dashboard tabs open. The wordmark still links home, which is where every
	// site puts that and is enough for somebody who reached /docs directly.
	b.WriteString(`<header><a class="brand" href="/">backup-maker</a> <span class="pill">docs</span></header>`)
	b.WriteString(`<div class="docs-layout"><nav class="docs-nav">`)
	b.WriteString(`<a class="docs-home` + activeClass(current, "README.md") + `" href="/docs">Start here</a>`)
	for _, sec := range docsNav() {
		b.WriteString(`<p class="docs-nav-heading">` + html.EscapeString(sec.Name) + `</p><ul>`)
		for _, p := range sec.Pages {
			b.WriteString(`<li><a class="` + strings.TrimSpace(activeClass(current, p.Path)) + `" href="/docs/` +
				html.EscapeString(p.Path) + `">` + html.EscapeString(p.Title) + `</a></li>`)
		}
		b.WriteString(`</ul>`)
	}
	b.WriteString(`</nav><main class="docs-body">` + body + `</main></div></body></html>`)
	return b.String()
}

func activeClass(current, p string) string {
	if current == p {
		return " active"
	}
	return ""
}
