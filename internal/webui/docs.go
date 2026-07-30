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

// docsSections are the tree's folders in the order the sidebar lists them.
var docsSections = []struct{ path, name string }{
	{"guide", "The guide"},
	{"setup", "Setting up hardware"},
	{"reference", "Reference"},
}

// docsPagesIn lists one folder's pages in reading order.
//
// That is the whole reason the guide's filenames are numbered: sorted order is
// reading order, so adding `7-something.md` puts it in the right place with no
// code change and no list to forget to update.
func docsPagesIn(fsys fs.FS, dir string) []string {
	entries, err := fs.ReadDir(fsys, dir)
	if err != nil {
		return nil
	}
	var pages []string
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
			continue
		}
		pages = append(pages, dir+"/"+e.Name())
	}
	sort.Strings(pages)
	return pages
}

// docsNav builds the sidebar for the embedded tree.
func docsNav() []docsSection { return docsNavIn(docsFiles()) }

// docsNavIn builds it by READING THE TREE rather than from a hardcoded list,
// which would drift the first time somebody added a page.
func docsNavIn(fsys fs.FS) []docsSection {
	if fsys == nil {
		return nil
	}
	out := []docsSection{}
	for _, dir := range docsSections {
		var pages []docsPage
		for _, p := range docsPagesIn(fsys, dir.path) {
			pages = append(pages, docsPage{Path: p, Title: docsTitle(fsys, p)})
		}
		if len(pages) > 0 {
			out = append(out, docsSection{Name: dir.name, Pages: pages})
		}
	}
	return out
}

// resolveDocsPage turns a link or a request path into an actual page in the
// tree, reporting false when there is no such page.
//
// Three shapes arrive here. "" is the index. An ordinary page may or may not
// carry its .md extension. And a DIRECTORY means "start reading here" — the
// documentation index opens with "read [the guide](guide/)", a link that
// returned 404 in this viewer for as long as the viewer has existed, because
// `guide/` is neither a Markdown file nor an image and the handler appended
// .md to it. Sending it to the folder's first page in reading order is what
// that link has always meant.
func resolveDocsPage(fsys fs.FS, rel string) (string, bool) {
	rel = strings.Trim(rel, "/")
	if rel == "" {
		return "README.md", true
	}
	if !fs.ValidPath(rel) {
		return "", false
	}
	if strings.HasSuffix(rel, ".md") {
		if _, err := fs.Stat(fsys, rel); err == nil {
			return rel, true
		}
		return "", false
	}
	if fi, err := fs.Stat(fsys, rel); err == nil && fi.IsDir() {
		if _, err := fs.Stat(fsys, rel+"/README.md"); err == nil {
			return rel + "/README.md", true
		}
		if pages := docsPagesIn(fsys, rel); len(pages) > 0 {
			return pages[0], true
		}
		return "", false
	}
	if _, err := fs.Stat(fsys, rel+".md"); err == nil {
		return rel + ".md", true
	}
	return "", false
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
	// they work on GitHub; rendered here they have to be readdressed. Matched
	// broadly rather than on a .md suffix, because `guide/` is a link too.
	docsHref = regexp.MustCompile(`href="([^"]*)"`)
	docsImg  = regexp.MustCompile(`src="([^"]+)"`)
)

// docsPaths is how one rendering of the documentation addresses itself.
//
// THERE ARE TWO RENDERINGS AND ONE RENDERER. Served from the daemon every link
// is absolute under /docs; exported to a directory the same pages are opened
// over file://, where an absolute link points at the root of the disk and a
// .md link downloads a file the browser will not display. The difference is
// entirely in how a tree path becomes an href, so that is the only thing that
// varies — a second copy of the renderer is how the two would drift, and the
// docs are the one place where being subtly out of date is invisible.
type docsPaths struct {
	prefix string // in front of a tree path
	home   string // where the wordmark goes
	assets string // in front of style.css and favicon.ico
	files  bool   // rewrite .md targets to the .html files an export writes
}

// servedDocsPaths addresses the documentation as the daemon serves it.
func servedDocsPaths() docsPaths {
	return docsPaths{prefix: "/docs/", home: "/", assets: "/"}
}

// exportedDocsPaths addresses it as a directory of files, relative to the page
// currently being written — so the whole export can be moved, copied onto a
// backup destination, or opened from a memory stick and still work.
func exportedDocsPaths(from string) docsPaths {
	up := ""
	if d := path.Dir(from); d != "." {
		up = strings.Repeat("../", strings.Count(d, "/")+1)
	}
	return docsPaths{prefix: up, home: up + "index.html", assets: up, files: true}
}

// href addresses one path in the tree.
func (p docsPaths) href(target string) string {
	if p.files {
		target = docsFileName(target)
	}
	return p.prefix + target
}

// docsFileName is a tree path as an export writes it: the index is index.html
// (what a browser opens for a directory), every other page keeps its name.
func docsFileName(p string) string {
	if !strings.HasSuffix(p, ".md") {
		return p
	}
	if p == "README.md" {
		return "index.html"
	}
	return strings.TrimSuffix(p, ".md") + ".html"
}

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

	rel := strings.Trim(strings.TrimPrefix(r.URL.Path, "/docs"), "/")
	// Serve images straight from the tree.
	if ext := path.Ext(rel); ext == ".png" || ext == ".jpg" || ext == ".jpeg" || ext == ".gif" || ext == ".svg" {
		serveDocsAsset(w, r, fsys, rel)
		return
	}
	// resolveDocsPage goes through fs.ValidPath, which rejects anything with
	// "..", an absolute path or a trailing slash — the whole of the traversal
	// defence. The tree is embedded and read-only, but a request must still not
	// be able to name its way out of it.
	page, ok := resolveDocsPage(fsys, rel)
	if !ok {
		http.Error(w, "no such page", http.StatusNotFound)
		return
	}
	rendered, err := renderDocsPage(fsys, page, servedDocsPaths())
	if err != nil {
		http.Error(w, "that page could not be rendered", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	fmt.Fprint(w, rendered)
}

// renderDocsPage turns one page of the tree into a complete HTML document,
// addressed however paths says. Both the served viewer and the export go
// through here, so neither can render something the other does not.
func renderDocsPage(fsys fs.FS, page string, paths docsPaths) (string, error) {
	raw, err := fs.ReadFile(fsys, page)
	if err != nil {
		return "", err
	}
	var body bytes.Buffer
	if err := md.Convert(raw, &body); err != nil {
		return "", err
	}
	return docsShell(fsys, page, rewriteDocsLinks(fsys, body.String(), path.Dir(page), paths), paths), nil
}

// rewriteDocsLinks turns the relative links that work on GitHub into ones that
// work in this rendering, resolving them against the page doing the linking.
func rewriteDocsLinks(fsys fs.FS, htmlBody, dir string, paths docsPaths) string {
	htmlBody = docsHref.ReplaceAllStringFunc(htmlBody, func(m string) string {
		target := docsHref.FindStringSubmatch(m)[1]
		// Left alone: anything off this site, and a bare fragment, which is a
		// link to a heading on the page being rendered.
		if target == "" || strings.HasPrefix(target, "#") ||
			strings.HasPrefix(target, "http") || strings.HasPrefix(target, "mailto:") {
			return m
		}
		// The fragment has to survive: many cross-references in these pages
		// point at a specific heading, not the top of a page.
		anchor := ""
		if i := strings.Index(target, "#"); i >= 0 {
			target, anchor = target[:i], target[i:]
		}
		cleaned := path.Clean(path.Join(dir, target))
		// Out of the tree entirely — `../README.md` from the index, say. Left
		// exactly as written: this renderer has no page to point it at, and
		// inventing one would turn a link somebody can see is external into a
		// broken internal one.
		if !fs.ValidPath(cleaned) {
			return m
		}
		page, ok := resolveDocsPage(fsys, cleaned)
		if !ok {
			// In the tree but not a page that exists — a typo, or a page
			// renamed without its links. Still readdressed, so the mistake is
			// a 404 inside the documentation rather than a link that escapes
			// it, and so an export does not ship an .md link no browser will
			// render.
			if !strings.HasSuffix(cleaned, ".md") {
				return m
			}
			page = cleaned
		}
		return `href="` + paths.href(page) + anchor + `"`
	})
	htmlBody = docsImg.ReplaceAllStringFunc(htmlBody, func(m string) string {
		src := docsImg.FindStringSubmatch(m)[1]
		if strings.HasPrefix(src, "http") || strings.HasPrefix(src, "/") {
			return m
		}
		return `src="` + paths.href(path.Clean(path.Join(dir, src))) + `"`
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
func docsShell(fsys fs.FS, current, body string, paths docsPaths) string {
	var b strings.Builder
	b.WriteString(`<!doctype html><html lang="en"><head><meta charset="utf-8">`)
	b.WriteString(`<meta name="viewport" content="width=device-width, initial-scale=1">`)
	b.WriteString(`<title>` + html.EscapeString(docsTitle(fsys, current)) + ` — backup-maker docs</title>`)
	b.WriteString(`<link rel="stylesheet" href="` + paths.assets + `style.css">`)
	b.WriteString(`<link rel="icon" href="` + paths.assets + `favicon.ico">`)
	b.WriteString(`</head><body class="docs">`)
	// No explicit "back to the dashboard" link: Docs opens in its own tab, so
	// there is nothing to go back FROM — following one would just leave two
	// dashboard tabs open. The wordmark still links home, which is where every
	// site puts that and is enough for somebody who reached /docs directly.
	b.WriteString(`<header><a class="brand" href="` + paths.home + `">backup-maker</a> <span class="pill">docs</span></header>`)
	b.WriteString(`<div class="docs-layout"><nav class="docs-nav">`)
	b.WriteString(`<a class="docs-home` + activeClass(current, "README.md") + `" href="` +
		paths.href("README.md") + `">Start here</a>`)
	for _, sec := range docsNavIn(fsys) {
		b.WriteString(`<p class="docs-nav-heading">` + html.EscapeString(sec.Name) + `</p><ul>`)
		for _, p := range sec.Pages {
			b.WriteString(`<li><a class="` + strings.TrimSpace(activeClass(current, p.Path)) + `" href="` +
				html.EscapeString(paths.href(p.Path)) + `">` + html.EscapeString(p.Title) + `</a></li>`)
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
