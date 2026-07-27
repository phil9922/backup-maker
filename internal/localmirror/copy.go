// SPDX-License-Identifier: MIT

package localmirror

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"io"
	"os"
	"path"
	"time"
)

// mtimeTolerance absorbs coarse timestamp granularity (FAT's 2 seconds, SMB
// server variance) so files don't recopy forever on such targets.
const mtimeTolerance = 2 * time.Second

const tmpSuffix = ".bmtmp"

// needsCopy compares a source file against its mirror by size and mtime.
func needsCopy(b Backend, srcInfo os.FileInfo, destPath string) bool {
	di, err := b.Stat(destPath)
	if err != nil {
		return true
	}
	if di.Size() != srcInfo.Size() {
		return true
	}
	delta := srcInfo.ModTime().Sub(di.ModTime())
	if delta < 0 {
		delta = -delta
	}
	return delta > mtimeTolerance
}

// progressWriter counts bytes on their way to the destination and reports the
// running total for the current attempt.
//
// It reports cumulative bytes rather than deltas on purpose: a retry starts the
// file over from zero, and an absolute figure simply rewinds where a delta
// would double-count the part already sent.
type progressWriter struct {
	w      io.Writer
	n      int64
	report func(written int64)
}

func (p *progressWriter) Write(b []byte) (int, error) {
	n, err := p.w.Write(b)
	p.n += int64(n)
	p.report(p.n)
	return n, err
}

// copyFile atomically mirrors srcPath (local absolute) to relPath on the
// backend: write a hidden temp file, sync, set the source mtime, then rename
// over the destination. Any existing destination is preserved in the version
// store first. When verify is set, the written file is read back and its
// SHA256 compared against the source stream (essential on network shares,
// which have no end-to-end integrity checking).
//
// progress, if non-nil, is called with the number of bytes written so far for
// this file. One very large file would otherwise leave the dashboard bar
// frozen until the whole thing landed, since everything else counts progress
// per completed file.
//
// written is how many bytes reached the destination on the attempt that
// SUCCEEDED, and is meaningful only when err is nil: a copy that dies halfway
// leaves nothing behind (the temp file is removed), so its bytes were never
// backed up and the lifetime odometer must not be told about them.
func copyFile(b Backend, srcPath, relPath string, now time.Time, verify bool, progress func(written int64)) (written int64, err error) {
	if err := b.MkdirAll(path.Dir(relPath)); err != nil {
		return 0, err
	}

	attempt := func() (int64, error) {
		if progress != nil {
			progress(0) // a retry re-sends the file from the start
		}
		src, err := os.Open(srcPath)
		if err != nil {
			return 0, err
		}
		defer src.Close()
		srcInfo, err := src.Stat()
		if err != nil {
			return 0, err
		}

		tmp := path.Join(path.Dir(relPath), "."+path.Base(relPath)+tmpSuffix)
		dst, err := b.OpenWrite(tmp)
		if err != nil {
			return 0, err
		}
		cleanup := func() { dst.Close(); _ = b.Remove(tmp) }

		hasher := sha256.New()
		var w io.Writer = dst
		if verify {
			w = io.MultiWriter(dst, hasher)
		}
		if progress != nil {
			w = &progressWriter{w: w, report: progress}
		}
		n, err := io.Copy(w, src)
		if err != nil {
			cleanup()
			return 0, fmt.Errorf("copying %s: %w", relPath, err)
		}
		if err := dst.Sync(); err != nil {
			cleanup()
			return 0, err
		}
		if err := dst.Close(); err != nil {
			_ = b.Remove(tmp)
			return 0, err
		}
		// Coarse-timestamp targets can't always represent the exact mtime;
		// the compare tolerance covers the gap.
		_ = b.Chtimes(tmp, time.Now(), srcInfo.ModTime())

		if verify {
			if err := verifyFile(b, tmp, hasher.Sum(nil)); err != nil {
				_ = b.Remove(tmp)
				return 0, err
			}
		}

		if _, err := b.Stat(relPath); err == nil {
			if err := keepVersion(b, relPath, now); err != nil {
				_ = b.Remove(tmp)
				return 0, err
			}
		}
		if err := b.Rename(tmp, relPath); err != nil {
			return 0, err
		}
		return n, nil
	}

	written, err = attempt()
	if err != nil && verify {
		// One retry: a transient network hiccup shouldn't cost the file.
		written, err = attempt()
	}
	return written, err
}

// verifyFile re-reads a written file and compares its SHA256 to want.
func verifyFile(b Backend, relPath string, want []byte) error {
	r, err := b.OpenRead(relPath)
	if err != nil {
		return fmt.Errorf("verification read of %s: %w", relPath, err)
	}
	defer r.Close()
	h := sha256.New()
	if _, err := io.Copy(h, r); err != nil {
		return fmt.Errorf("verification read of %s: %w", relPath, err)
	}
	if !bytes.Equal(h.Sum(nil), want) {
		return fmt.Errorf("verification failed for %s: written data does not match source", relPath)
	}
	return nil
}
