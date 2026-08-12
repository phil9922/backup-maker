// SPDX-License-Identifier: MIT

// Package archive creates scheduled FULL backups: AES-256 password-protected
// zip snapshots of selected folders, written to a drive or network-share
// target and pruned by a retention count. Complements the real-time mirror —
// a mirror follows every change; an archive freezes a moment in time.
package archive

import (
	"bufio"
	"errors"
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

// PathFor is the directory one snapshot job writes into on a destination, both
// parts made safe for FAT, NTFS and SMB.
//
// EXPORTED SO THERE IS EXACTLY ONE OF IT, for the reason config.DestRoot was:
// this expression used to live inline in Run(), which was fine while the writer
// was the only thing that needed to know. It is not fine now that the file view
// classifies a directory on a destination by asking which job wrote it, and
// offers to delete the ones no job did. A second, subtly different spelling of
// the same path is how such a delete decides a live schedule's snapshots belong
// to nobody.
func PathFor(machineName, jobName string) string {
	return path.Join(DirName, sanitize(machineName), sanitize(jobName))
}

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
	StoredBytes int64 `json:"stored_bytes,omitempty"`
	// Unstable names files that were being written WHILE they were read into
	// this snapshot, so their copies may be torn.
	//
	// A zip is sealed for ever, which is what makes this worth saying out loud.
	// A mirror that copies a file mid-write fixes itself on the next pass; a
	// snapshot keeps that copy until it is deleted, and the first anyone would
	// otherwise learn of it is the day they try to open it on another machine
	// and it will not load. Naming it here is the difference between a backup
	// that is imperfect and a backup that is imperfect AND silent.
	Unstable []string `json:"unstable,omitempty"`
	// Unverified is why this snapshot was written WITHOUT being proved
	// restorable, and is empty on a run that was checked (and on one that
	// failed, which wrote nothing to check).
	//
	// VERIFICATION IS THE PROMISE, so its absence has to travel with the result.
	// A finished snapshot is normally read back off the destination and every
	// entry decrypted, which is what lets this program say the backup can
	// actually be restored. That read spools the whole archive to a local temp
	// file, and when there is not room for it the snapshot is still written —
	// tens of gigabytes of somebody's files are worth far more than the check —
	// but a run that quietly did less than it claims is exactly the thing this
	// project alerts about. status turns this into a flag on the schedule's row,
	// and the daemon into a notification.
	Unverified string `json:"unverified,omitempty"`
	Err        string `json:"err,omitempty"`
}

// maxUnstableNamed bounds the list: a folder where everything is being
// rewritten should not produce a log line with fifty thousand paths in it.
const maxUnstableNamed = 20

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

	dir := PathFor(cfg.General.MachineName, job.Name)
	if err := b.MkdirAll(dir); err != nil {
		return fail(fmt.Errorf("creating %s on target: %w", dir, err))
	}
	stamp := res.When.Format(stampLayout)
	final := path.Join(dir, sanitize(job.Name)+"-"+stamp+".zip")
	tmp := final + ".bmtmp"

	// BEFORE WRITING, NOT AFTER. Stranded temps were already swept — but by
	// prune(), which runs at the END of a successful run. The failure mode is a
	// run that never finishes: every restart abandons a part-written zip, and
	// nothing ever reaches the sweep, so they pile up unbounded. Three reached
	// 8.4GB on one SD card with not a single completed snapshot to show for it,
	// and two more totalling 2.6GB turned up on the Pi a day later.
	//
	// Sweeping here bounds it at one, whatever happens. A destination that is
	// filling up with the leftovers of a snapshot that cannot complete is the
	// worst possible place to be spending the space.
	sweepStrandedTemps(b, dir, tmp, log)

	// AND ONLY THEN ASK WHETHER IT FITS — after the sweep, because the space a
	// stranded temp was holding is space this run can have. A snapshot that will
	// not fit deletes this job's own oldest snapshots until it does, and if it
	// still will not fit the run stops HERE, before a single byte is written.
	zips := jobZips(b, dir)
	need := uint64(estimateSnapshotBytes(cfg, job, folders, zips))
	if err := ensureRoomFor(b, cfg, job, zips, need, log); err != nil {
		return fail(err)
	}

	// AND WHETHER THERE IS ROOM TO CHECK IT AFTERWARDS — asked here, before
	// packing, and not at the point it is needed. Verification spools the whole
	// archive to a LOCAL temp file, and packing 57GB over wifi takes two hours:
	// discovering at the end that the spool will not fit throws all of that away,
	// and doing it anyway fills the disk the source folders live on. A shortfall
	// does not fail the run — the snapshot is written and kept, and says on its
	// Result that it was not checked.
	spoolDir, unverified := planSpool(cfg, job, need, log)

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
	files, bytes, unstable, unstableCount, err := writeZip(buffered, cfg, job, folders, password, report)
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
	if unverified == "" {
		err := verifyZip(b, tmp, password, files, spoolDir, func(read int64) {
			if report != nil {
				report(Progress{
					Phase: PhaseVerifying, DoneFiles: files, TotalFiles: files,
					DoneBytes: read, TotalBytes: stored.n,
				})
			}
		})
		// THE LOCAL DISK RUNNING SHORT IS NOT A FAILED SNAPSHOT. The zip on the
		// destination is complete; only the reading back was abandoned, and it was
		// abandoned precisely so the disk holding the source folders is not filled.
		// Throwing the archive away here would destroy hours of work to punish a
		// shortage somewhere else entirely.
		var full *spoolFullError
		switch {
		case errors.As(err, &full):
			unverified = "the snapshot was written but not checked: " + full.Error()
			log.Warn("this snapshot was written but NOT checked: the disk it was being read back onto was running out of room",
				"archive", job.Name, "spool_dir", full.dir, "free_bytes", full.free)
		case err != nil:
			_ = b.Remove(tmp)
			return fail(fmt.Errorf("verification: %w", err))
		}
	}
	if err := b.Rename(tmp, final); err != nil {
		_ = b.Remove(tmp)
		return fail(err)
	}

	res.File, res.Files, res.Bytes, res.StoredBytes = final, files, bytes, stored.n
	res.Unstable = unstable
	res.Unverified = unverified
	log.Info("archive written", "archive", job.Name, "file", final,
		"files", files, "bytes", bytes, "stored_bytes", stored.n,
		"checked", unverified == "")
	if unverified != "" {
		// Said again, at the end, next to the line that says the snapshot exists:
		// the warning that decided this was logged before packing began, which on
		// a real snapshot is hours earlier and thousands of lines away.
		log.Warn("this snapshot has NOT been proved restorable", "archive", job.Name,
			"file", final, "why", unverified)
	}
	if unstableCount > 0 {
		log.Warn("some files were being written while this snapshot read them, so their copies may be incomplete",
			"archive", job.Name, "count", unstableCount, "files", unstable)
	}

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
func writeZip(w io.Writer, cfg *config.Config, job config.Archive, folders []config.Folder, password string, report func(Progress)) (files int, total int64, unstable []string, unstableCount int, err error) {
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
		// Stat BEFORE reading, so the comparison after is against the state
		// the read actually started from.
		before, serr := src.Stat()
		n, cerr := io.Copy(entry, src)
		if cerr != nil {
			return cerr
		}
		// CHANGED WHILE WE WERE READING IT? Then what went into the zip is
		// part of one version and part of another. There is no retry to be had:
		// the entry is already written, and a file being rewritten continuously
		// would only tear again. Recording it is the honest answer.
		if serr == nil {
			if after, aerr := os.Stat(p); aerr == nil &&
				(after.Size() != before.Size() || !after.ModTime().Equal(before.ModTime())) {
				if len(unstable) < maxUnstableNamed {
					unstable = append(unstable, sanitize(f.Label)+"/"+rel)
				}
				unstableCount++
			}
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
		return files, total, unstable, unstableCount, werr
	}
	return files, total, unstable, unstableCount, zw.Close()
}

// verifyZip re-reads the written archive from the target (spooled to a local
// temp file so memory use stays flat), checks the entry count, and fully
// decrypts every entry — proof the backup is restorable with the password
// before we keep it.
//
// spoolDir is where that local copy goes, chosen by planSpool before packing
// started and never blank in a real run. THE SPOOL IS THE WHOLE ARCHIVE, so the
// disk under it is measured as it is written and the copy is abandoned rather
// than allowed to fill it — a *spoolFullError, which the caller treats as an
// unchecked snapshot rather than a failed one.
func verifyZip(b localmirror.Backend, relPath, password string, wantFiles int, spoolDir string, onRead func(int64)) error {
	src, err := b.OpenRead(relPath)
	if err != nil {
		return err
	}
	defer src.Close()
	spool, err := os.CreateTemp(spoolDir, "backup-maker-verify-*.zip")
	if err != nil {
		return err
	}
	// Removed on every path out, including the one where the disk ran short:
	// leaving tens of gigabytes of abandoned spool behind on a disk that is
	// already running out is the failure this is guarding against.
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
	size, err := io.CopyBuffer(newSpoolGuard(spool, filepath.Dir(spool.Name()), minFreeFloorBytes),
		r, make([]byte, writeBufferSize))
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

// sweepStrandedTemps removes part-written zips left by runs that never finished,
// except the one this run is about to write.
//
// WHY EVERY OTHER TEMP IN HERE IS SAFE TO DELETE. Snapshot jobs are run from one
// goroutine, one at a time (daemon.archiveLoop → runDueArchives → runArchive, all
// synchronous, and archive.Run has no other caller), so no second run of this job
// can be writing while this one starts. The directory is per machine AND per job,
// and a machine name is claimed exclusively on a destination, so nothing else
// writes here either. If a second concurrent caller is ever added, this stops
// being true and has to grow an age test like the mirror's staleTempAge.
//
// Failures are logged and ignored: a snapshot must still run on a destination
// that will not let go of an old temp.
func sweepStrandedTemps(b localmirror.Backend, dir, keep string, log *slog.Logger) {
	_ = b.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || p == keep || !strings.HasSuffix(p, ".bmtmp") {
			return nil
		}
		if err := b.Remove(p); err != nil {
			log.Debug("could not remove an abandoned snapshot temp file", "path", p, "err", err)
			return nil
		}
		log.Info("removed an abandoned snapshot temp file left by an interrupted run", "path", p)
		return nil
	})
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
