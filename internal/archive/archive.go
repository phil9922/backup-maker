// SPDX-License-Identifier: MIT

// Package archive creates scheduled FULL backups: AES-256 password-protected
// zip snapshots of selected folders, written to a drive or network-share
// target and pruned by a retention count. Complements the real-time mirror —
// a mirror follows every change; an archive freezes a moment in time.
package archive

import (
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"

	zip "github.com/yeka/zip"

	"github.com/phil9922/backup-maker/internal/config"
	"github.com/phil9922/backup-maker/internal/localmirror"
)

// DirName is where archives live on a target, mirroring the layout of the
// mirror engine: <target root>/backup-maker-archives/<machine>/<job>/.
const DirName = "backup-maker-archives"

const stampLayout = "20060102-150405"

// Result summarizes one archive run for status displays.
type Result struct {
	ArchiveName string    `json:"archive_name"`
	When        time.Time `json:"when"`
	File        string    `json:"file,omitempty"`
	Files       int       `json:"files"`
	Bytes       int64     `json:"bytes"`
	Err         string    `json:"err,omitempty"`
}

// Run builds one encrypted snapshot for the job and writes it to the backend.
// The password is mandatory: this function refuses to write an unprotected
// archive. Every entry is re-read from the target and decrypted afterwards
// to verify the archive is actually restorable.
func Run(b localmirror.Backend, cfg *config.Config, job config.Archive, password string, log *slog.Logger) (res Result) {
	res = Result{ArchiveName: job.Name, When: time.Now()}
	fail := func(err error) Result {
		res.Err = err.Error()
		log.Error("archive failed", "archive", job.Name, "err", err)
		return res
	}
	if password == "" {
		return fail(fmt.Errorf("no password stored — refusing to write an unprotected archive; run: backup-maker wizard"))
	}

	folders := cfg.FoldersForArchive(job)
	if len(folders) == 0 {
		return fail(fmt.Errorf("no folders selected"))
	}

	dir := path.Join(DirName, sanitize(cfg.General.MachineName), sanitize(job.Name))
	if err := b.MkdirAll(dir); err != nil {
		return fail(fmt.Errorf("creating %s on target: %w", dir, err))
	}
	stamp := res.When.Format(stampLayout)
	final := path.Join(dir, sanitize(job.Name)+"-"+stamp+".zip")
	tmp := final + ".bmtmp"

	w, err := b.OpenWrite(tmp)
	if err != nil {
		return fail(err)
	}
	files, bytes, err := writeZip(w, cfg, job, folders, password)
	if cerr := w.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		_ = b.Remove(tmp)
		return fail(err)
	}
	if err := verifyZip(b, tmp, password, files); err != nil {
		_ = b.Remove(tmp)
		return fail(fmt.Errorf("verification: %w", err))
	}
	if err := b.Rename(tmp, final); err != nil {
		_ = b.Remove(tmp)
		return fail(err)
	}

	res.File, res.Files, res.Bytes = final, files, bytes
	log.Info("archive written", "archive", job.Name, "file", final, "files", files, "bytes", bytes)

	keep := job.Keep
	if keep <= 0 {
		keep = config.DefaultArchiveKeep
	}
	if err := prune(b, dir, keep); err != nil {
		log.Warn("archive retention prune failed", "archive", job.Name, "err", err)
	}
	return res
}

// writeZip streams every selected folder into an AES-256 encrypted zip.
func writeZip(w io.Writer, cfg *config.Config, job config.Archive, folders []config.Folder, password string) (files int, total int64, err error) {
	zw := zip.NewWriter(w)
	for _, f := range folders {
		// A snapshot may deliberately keep what the mirror drops: the folder's
		// exclude list is shared with the live mirror, so without this a
		// complete sealed archive would be impossible whenever the mirror is
		// deliberately lean.
		var pats []string
		if !f.NoDefaultIgnores && !job.NoDefaultIgnores {
			pats = append(pats, cfg.Defaults.Ignore...)
		}
		if !job.NoDefaultIgnores {
			pats = append(pats, f.ExtraIgnore...)
		}
		pats = append(pats, job.ExtraIgnore...)
		matcher := localmirror.NewMatcher(append(pats, ".stfolder", ".stignore", ".stversions"))

		root := f.Path
		label := sanitize(f.Label)
		werr := filepath.WalkDir(root, func(p string, d fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return nil // skip unreadable entries; archive what we can
			}
			rel, rerr := filepath.Rel(root, p)
			if rerr != nil || rel == "." {
				return nil
			}
			rel = filepath.ToSlash(rel)
			if matcher.Ignored(rel) {
				if d.IsDir() {
					return filepath.SkipDir
				}
				return nil
			}
			if d.IsDir() || !d.Type().IsRegular() {
				return nil
			}
			src, oerr := os.Open(p)
			if oerr != nil {
				return nil
			}
			defer src.Close()
			entry, zerr := zw.Encrypt(label+"/"+rel, password, zip.AES256Encryption)
			if zerr != nil {
				return zerr
			}
			n, cerr := io.Copy(entry, src)
			if cerr != nil {
				return cerr
			}
			files++
			total += n
			return nil
		})
		if werr != nil {
			zw.Close()
			return files, total, werr
		}
	}
	return files, total, zw.Close()
}

// verifyZip re-reads the written archive from the target (spooled to a local
// temp file so memory use stays flat), checks the entry count, and fully
// decrypts every entry — proof the backup is restorable with the password
// before we keep it.
func verifyZip(b localmirror.Backend, relPath, password string, wantFiles int) error {
	src, err := b.OpenRead(relPath)
	if err != nil {
		return err
	}
	defer src.Close()
	spool, err := os.CreateTemp("", "backup-maker-verify-*.zip")
	if err != nil {
		return err
	}
	defer os.Remove(spool.Name())
	defer spool.Close()
	size, err := io.Copy(spool, src)
	if err != nil {
		return err
	}
	zr, err := zip.NewReader(spool, size)
	if err != nil {
		return err
	}
	if len(zr.File) != wantFiles {
		return fmt.Errorf("archive has %d entries, expected %d", len(zr.File), wantFiles)
	}
	for _, zf := range zr.File {
		zf.SetPassword(password)
		rc, err := zf.Open()
		if err != nil {
			return fmt.Errorf("cannot decrypt %s: %w", zf.Name, err)
		}
		_, err = io.Copy(io.Discard, rc)
		rc.Close()
		if err != nil {
			return fmt.Errorf("cannot decrypt %s: %w", zf.Name, err)
		}
	}
	return nil
}

// prune keeps the newest keep archives in dir (stamps sort lexically).
func prune(b localmirror.Backend, dir string, keep int) error {
	var zips []string
	err := b.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		if strings.HasSuffix(p, ".zip") {
			zips = append(zips, p)
		}
		if strings.HasSuffix(p, ".bmtmp") {
			_ = b.Remove(p) // stranded temp from an interrupted run
		}
		return nil
	})
	if err != nil {
		return err
	}
	sort.Strings(zips) // timestamped names: oldest first
	for len(zips) > keep {
		if err := b.Remove(zips[0]); err != nil {
			return err
		}
		zips = zips[1:]
	}
	return nil
}

func sanitize(name string) string {
	out := []rune(name)
	for i, r := range out {
		switch r {
		case '<', '>', ':', '"', '/', '\\', '|', '?', '*':
			out[i] = '_'
		}
	}
	if len(out) == 0 {
		return "unnamed"
	}
	return string(out)
}
