// SPDX-License-Identifier: MIT

// Package archive creates scheduled FULL backups: AES-256 password-protected
// zip snapshots of selected folders, written to a drive or network-share
// target and pruned by a retention count. Complements the real-time mirror —
// a mirror follows every change; an archive freezes a moment in time.
package archive

import (
	"bufio"
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

// writeBufferSize is how much is gathered before anything is handed to the
// destination. 1MB because that is the largest a single SMB2 write carries in
// practice; larger just gets split again on the way out, smaller means more
// round trips for the same bytes.
const writeBufferSize = 1 << 20

// Result summarizes one archive run for status displays.
type Result struct {
	ArchiveName string    `json:"archive_name"`
	When        time.Time `json:"when"`
	File        string    `json:"file,omitempty"`
	Files       int       `json:"files"`
	Bytes       int64     `json:"bytes"`
	// StoredBytes is the size of the snapshot as it sits on the destination,
	// which is NOT Bytes: the zip is compressed, so the source total above is
	// larger. The lifetime odometer takes this one, because a figure headed
	// "backed up in total" that quoted the uncompressed size would claim more
	// data was written than the destination ever received.
	//
	// Set only on a run that completed and verified; a failed run leaves it 0.
	StoredBytes int64  `json:"stored_bytes,omitempty"`
	Err         string `json:"err,omitempty"`
}

// Run builds one encrypted snapshot for the job and writes it to the backend.
// The password is mandatory: this function refuses to write an unprotected
// archive. Every entry is re-read from the target and decrypted afterwards
// to verify the archive is actually restorable.
//
// report, if non-nil, is called as the snapshot is packed — first with the
// totals from the pre-pass, then once per file. It is what lets the dashboard
// draw a real bar for a job that takes half an hour rather than a word that
// does not change.
func Run(b localmirror.Backend, cfg *config.Config, job config.Archive, password string, log *slog.Logger, report func(Progress)) (res Result) {
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
	// Counted on the way out rather than stat'ed afterwards: it is exact, and
	// it costs nothing extra on a destination reached over SMB.
	stored := &countingWriter{w: w}
	// BUFFERED, because the destination writer is the network.
	//
	// An SMB backend turns every Write into its own request and waits for it,
	// and the zip's compressor emits small blocks — so the snapshot was paying
	// a round trip per block. Measured against a Raspberry Pi on ethernet, from
	// a laptop with a 526 Mbit/s link and the Pi at load 0.00, that came to
	// about 2 MB/s: neither end was busy, the job was simply waiting for the
	// network tens of thousands of times a second.
	buffered := bufio.NewWriterSize(stored, writeBufferSize)
	files, bytes, err := writeZip(buffered, cfg, job, folders, password, report)
	if err == nil {
		err = buffered.Flush() // before Close, or the tail is never sent
	}
	if cerr := w.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		_ = b.Remove(tmp)
		return fail(err)
	}
	if err := verifyZip(b, tmp, password, files, func(read int64) {
		if report != nil {
			report(Progress{
				Phase: PhaseVerifying, DoneFiles: files, TotalFiles: files,
				DoneBytes: read, TotalBytes: stored.n,
			})
		}
	}); err != nil {
		_ = b.Remove(tmp)
		return fail(fmt.Errorf("verification: %w", err))
	}
	if err := b.Rename(tmp, final); err != nil {
		_ = b.Remove(tmp)
		return fail(err)
	}

	res.File, res.Files, res.Bytes, res.StoredBytes = final, files, bytes, stored.n
	log.Info("archive written", "archive", job.Name, "file", final,
		"files", files, "bytes", bytes, "stored_bytes", stored.n)

	keep := job.Keep
	if keep <= 0 {
		keep = config.DefaultArchiveKeep
	}
	if err := prune(b, dir, keep); err != nil {
		log.Warn("archive retention prune failed", "archive", job.Name, "err", err)
	}
	return res
}

// countingReader reports how much of the archive has been read back, so the
// verification phase can show progress instead of a full bar that does not move.
type countingReader struct {
	r      io.Reader
	n      int64
	report func(int64)
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n += int64(n)
	c.report(c.n)
	return n, err
}

// countingWriter totals the bytes handed to the destination, so the size of the
// finished snapshot is known without reading it back.
type countingWriter struct {
	w io.Writer
	n int64
}

func (c *countingWriter) Write(p []byte) (int, error) {
	n, err := c.w.Write(p)
	c.n += int64(n)
	return n, err
}

// Progress reports how far a snapshot has got. Total is what the pre-pass
// counted; Done rises as entries are packed.
//
// Bytes are SOURCE bytes, not bytes on the destination: the zip is compressed,
// so measuring the output against a source total would draw a bar that reached
// the end early and stopped. Compression ratio is not known in advance and
// varies wildly across one folder — 2.7GB of images beside 1.4GB of source —
// so the honest denominator is the one thing known before any work starts.
type Progress struct {
	// Phase is "packing" while files are being compressed into the zip, and
	// "verifying" while the finished archive is read back and decrypted.
	//
	// VERIFICATION IS NOT A ROUNDING ERROR AT THE END. It re-reads every byte
	// of the archive from the destination — for a 3.4GB snapshot over a network
	// that was ten minutes, during which the bar sat full at 100% and the state
	// still said "running". It looked hung, and was reported as hung, while it
	// was doing exactly what it promises: proving the backup can be opened
	// again before keeping it.
	Phase      string
	DoneFiles  int
	TotalFiles int
	DoneBytes  int64
	TotalBytes int64
}

// Phases a snapshot passes through.
const (
	PhasePacking   = "packing"
	PhaseVerifying = "verifying"
)

// eachFile calls fn for every file a snapshot of these folders would contain.
//
// THE PRE-PASS AND THE PACK SHARE THIS, and that is the whole point: a total
// counted by any other walk is a different question being answered, and the bar
// would fill against a denominator that does not match the work. One place
// decides what is in a snapshot.
func eachFile(cfg *config.Config, job config.Archive, folders []config.Folder,
	fn func(f config.Folder, absPath, rel string, d fs.DirEntry) error) error {
	for _, f := range folders {
		// A snapshot may deliberately keep what the mirror drops: the folder's
		// exclude list is shared with the live mirror, so without this a
		// complete sealed archive would be impossible whenever the mirror is
		// deliberately lean. NoDefaultIgnores reaches the junk patterns only —
		// NewMatcherFor still keeps our own configuration directory out, which
		// matters most here: the password that decrypts this zip lives in it.
		var pats []string
		if !f.NoDefaultIgnores && !job.NoDefaultIgnores {
			pats = append(pats, cfg.Defaults.Ignore...)
		}
		if !job.NoDefaultIgnores {
			pats = append(pats, f.ExtraIgnore...)
		}
		pats = append(pats, job.ExtraIgnore...)

		root := f.Path
		matcher := localmirror.NewMatcherFor(root, append(pats, ".stfolder", ".stignore", ".stversions"))

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
			return fn(f, p, rel, d)
		})
		if werr != nil {
			return werr
		}
	}
	return nil
}

// measure counts what the snapshot will contain, so the bar has a real
// denominator. One local walk with no reads — cheap beside compressing the
// same files, and the only way to say "1.2GB of 4.1GB" rather than a spinner.
func measure(cfg *config.Config, job config.Archive, folders []config.Folder) (files int, bytes int64) {
	_ = eachFile(cfg, job, folders, func(_ config.Folder, _, _ string, d fs.DirEntry) error {
		info, err := d.Info()
		if err != nil {
			return nil // unreadable now, skipped by the pack too
		}
		files++
		bytes += info.Size()
		return nil
	})
	return files, bytes
}

// writeZip streams every selected folder into an AES-256 encrypted zip.
func writeZip(w io.Writer, cfg *config.Config, job config.Archive, folders []config.Folder, password string, report func(Progress)) (files int, total int64, err error) {
	zw := zip.NewWriter(w)
	prog := Progress{Phase: PhasePacking}
	if report != nil {
		prog.TotalFiles, prog.TotalBytes = measure(cfg, job, folders)
		report(prog)
	}
	werr := eachFile(cfg, job, folders, func(f config.Folder, p, rel string, _ fs.DirEntry) error {
		src, oerr := os.Open(p)
		if oerr != nil {
			return nil
		}
		defer src.Close()
		entry, zerr := zw.Encrypt(sanitize(f.Label)+"/"+rel, password, zip.AES256Encryption)
		if zerr != nil {
			return zerr
		}
		n, cerr := io.Copy(entry, src)
		if cerr != nil {
			return cerr
		}
		files++
		total += n
		if report != nil {
			// Reported per file rather than per chunk: a snapshot is tens of
			// thousands of small files, and the dashboard polls once a second.
			prog.DoneFiles, prog.DoneBytes = files, total
			report(prog)
		}
		return nil
	})
	if werr != nil {
		zw.Close()
		return files, total, werr
	}
	return files, total, zw.Close()
}

// verifyZip re-reads the written archive from the target (spooled to a local
// temp file so memory use stays flat), checks the entry count, and fully
// decrypts every entry — proof the backup is restorable with the password
// before we keep it.
func verifyZip(b localmirror.Backend, relPath, password string, wantFiles int, onRead func(int64)) error {
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
	// Same reason the write side is buffered: reading back over SMB in the
	// default 32KB chunks is 32 round trips per megabyte, and verification
	// re-reads the WHOLE archive — for a 3.4GB snapshot that was the slower
	// half of the job.
	var r io.Reader = src
	if onRead != nil {
		r = &countingReader{r: src, report: onRead}
	}
	size, err := io.CopyBuffer(spool, r, make([]byte, writeBufferSize))
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
