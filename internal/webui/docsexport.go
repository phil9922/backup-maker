// SPDX-License-Identifier: MIT

package webui

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/phil9922/backup-maker/internal/version"
)

// exportMarker is written at the root of an export and is what lets a later
// export recognise its own output.
//
// THE RECOGNITION GATE, the same shape the destination markers use. An export
// writes a dozen ordinary filenames — index.html, style.css, a screenshots
// directory — into whatever directory it is pointed at, and `--export .` in
// the wrong terminal is one keystroke away. So a directory that already has
// something in it is refused unless this file says the something is ours.
// Nothing is ever deleted either way; the refusal is about not scattering files
// through somebody's work, and the marker is what makes re-exporting to the
// same place the ordinary thing it should be.
const exportMarker = ".backup-maker-docs"

// ExportedDocs reports what an export wrote.
type ExportedDocs struct {
	Dir    string
	Pages  int
	Assets int
}

// DocsFile is one file of the exported manual: a slash-separated path relative
// to the root of the export, and the bytes that go in it.
type DocsFile struct {
	Name string
	Data []byte
	// Page marks a rendered documentation page rather than an asset carried
	// along beside it, which is the only distinction any caller has needed —
	// "16 pages and 15 files" is what an export reports.
	Page bool
}

// BuildDocs renders the whole manual into memory as the files an export writes.
//
// IN MEMORY, AND ON PURPOSE. The same set of files is written to more than one
// place — a directory the user named, and every backup destination — and the
// pages are a pure function of the build, so rendering them once per write
// target would be the same work repeated. It is a couple of megabytes, almost
// all of it screenshots, held only for as long as it takes to write them.
//
// Nothing here touches the filesystem, so the two writers cannot drift in what
// they produce; they differ only in where it goes and what they are allowed to
// overwrite.
func BuildDocs() ([]DocsFile, error) {
	fsys := docsFiles()
	if fsys == nil {
		return nil, errors.New("this build carries no documentation to export")
	}
	var out []DocsFile
	err := fs.WalkDir(fsys, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		if !strings.HasSuffix(p, ".md") {
			raw, err := fs.ReadFile(fsys, p)
			if err != nil {
				return err
			}
			out = append(out, DocsFile{Name: docsFileName(p), Data: raw})
			return nil
		}
		page, err := renderDocsPage(fsys, p, exportedDocsPaths(p))
		if err != nil {
			return fmt.Errorf("%s: %w", p, err)
		}
		out = append(out, DocsFile{Name: docsFileName(p), Data: []byte(page), Page: true})
		return nil
	})
	if err != nil {
		return nil, err
	}
	// The pages are styled by the dashboard's own stylesheet, so it travels
	// with them; without it the export is readable but looks like nothing.
	for _, name := range []string{"style.css", "favicon.ico"} {
		raw, err := fs.ReadFile(staticFS, path.Join("static", name))
		if err != nil {
			return nil, err
		}
		out = append(out, DocsFile{Name: name, Data: raw})
	}
	return out, nil
}

// DocsFingerprint identifies the manual this build carries, so somewhere
// holding a copy can tell whether it is the current one without comparing two
// megabytes of files.
//
// COMPUTED FROM THE DOCUMENTATION ITSELF, not from the version string. A build
// that changes a page keeps the same version number for as long as it is
// unreleased, and the copy on a destination would then never be replaced — the
// one failure mode where being out of date is invisible, because a manual looks
// exactly as convincing whether or not it describes this program. The build is
// mixed in as well, since the renderer is Go code that the pages depend on.
//
// It hashes the sources rather than the rendered output so nothing has to be
// rendered to answer the question; it is asked periodically and answered "yes,
// that is what is already there" nearly every time.
func DocsFingerprint() (string, error) {
	fsys := docsFiles()
	if fsys == nil {
		return "", errors.New("this build carries no documentation")
	}
	h := sha256.New()
	v := version.Get()
	fmt.Fprintf(h, "build\x00%s\x00%s\x00", v.Version, v.Commit)
	err := fs.WalkDir(fsys, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		raw, err := fs.ReadFile(fsys, p)
		if err != nil {
			return err
		}
		fmt.Fprintf(h, "doc\x00%s\x00%d\x00", p, len(raw))
		h.Write(raw)
		return nil
	})
	if err != nil {
		return "", err
	}
	for _, name := range []string{"style.css", "favicon.ico"} {
		raw, err := fs.ReadFile(staticFS, path.Join("static", name))
		if err != nil {
			return "", err
		}
		fmt.Fprintf(h, "asset\x00%s\x00%d\x00", name, len(raw))
		h.Write(raw)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// ExportDocs writes the documentation to dir as a directory of HTML files that
// need no daemon, no network and no web server — the manual, readable from a
// clone, a memory stick, or a backup destination beside the status page.
//
// It reuses the renderer the dashboard uses rather than shelling out to
// anything: same Markdown, same stylesheet, same sidebar, addressed relatively
// so the result can be moved anywhere. See docsPaths.
func ExportDocs(dir string) (*ExportedDocs, error) {
	files, err := BuildDocs()
	if err != nil {
		return nil, err
	}
	if dir == "" {
		return nil, errors.New("say where to write the documentation")
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	if err := readyExportDir(abs); err != nil {
		return nil, err
	}

	out := &ExportedDocs{Dir: abs}
	for _, f := range files {
		dest := filepath.Join(abs, filepath.FromSlash(f.Name))
		if parent := filepath.Dir(dest); parent != abs {
			if err := os.MkdirAll(parent, 0o755); err != nil {
				return nil, err
			}
		}
		if err := os.WriteFile(dest, f.Data, 0o644); err != nil {
			return nil, err
		}
		if f.Page {
			out.Pages++
		} else {
			out.Assets++
		}
	}
	return out, nil
}

// readyExportDir creates the directory, or accepts an existing one only when it
// is empty or holds a previous export. See exportMarker.
func readyExportDir(abs string) error {
	entries, err := os.ReadDir(abs)
	switch {
	case errors.Is(err, os.ErrNotExist):
		if err := os.MkdirAll(abs, 0o755); err != nil {
			return err
		}
		// Marked on the way in, not on the way out: an export interrupted
		// half-written must still be recognised as ours next time, or the
		// retry is refused by the files the failed attempt left behind.
		return writeExportMarker(abs)
	case err != nil:
		return err
	}
	if len(entries) == 0 {
		return writeExportMarker(abs)
	}
	if _, err := os.Stat(filepath.Join(abs, exportMarker)); err == nil {
		return nil // ours; overwrite in place
	}
	return fmt.Errorf("%s already has files in it that were not put there by an export; "+
		"choose an empty directory or a new one", abs)
}

func writeExportMarker(abs string) error {
	return os.WriteFile(filepath.Join(abs, exportMarker),
		[]byte("This directory holds documentation exported by backup-maker.\n"+
			"Exporting here again overwrites these files. Delete this marker and\n"+
			"backup-maker will refuse to write here.\n"), 0o644)
}
