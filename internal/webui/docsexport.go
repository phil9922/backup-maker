// SPDX-License-Identifier: MIT

package webui

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
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

// ExportDocs writes the documentation to dir as a directory of HTML files that
// need no daemon, no network and no web server — the manual, readable from a
// clone, a memory stick, or a backup destination beside the status page.
//
// It reuses the renderer the dashboard uses rather than shelling out to
// anything: same Markdown, same stylesheet, same sidebar, addressed relatively
// so the result can be moved anywhere. See docsPaths.
func ExportDocs(dir string) (*ExportedDocs, error) {
	fsys := docsFiles()
	if fsys == nil {
		return nil, errors.New("this build carries no documentation to export")
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
	err = fs.WalkDir(fsys, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if p == "." {
				return nil
			}
			return os.MkdirAll(filepath.Join(abs, filepath.FromSlash(p)), 0o755)
		}
		dest := filepath.Join(abs, filepath.FromSlash(docsFileName(p)))
		if !strings.HasSuffix(p, ".md") {
			raw, err := fs.ReadFile(fsys, p)
			if err != nil {
				return err
			}
			out.Assets++
			return os.WriteFile(dest, raw, 0o644)
		}
		page, err := renderDocsPage(fsys, p, exportedDocsPaths(p))
		if err != nil {
			return fmt.Errorf("%s: %w", p, err)
		}
		out.Pages++
		return os.WriteFile(dest, []byte(page), 0o644)
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
		if err := os.WriteFile(filepath.Join(abs, name), raw, 0o644); err != nil {
			return nil, err
		}
		out.Assets++
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
