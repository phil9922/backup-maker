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

// copyFile atomically mirrors srcPath (local absolute) to relPath on the
// backend: write a hidden temp file, sync, set the source mtime, then rename
// over the destination. Any existing destination is preserved in the version
// store first. When verify is set, the written file is read back and its
// SHA256 compared against the source stream (essential on network shares,
// which have no end-to-end integrity checking).
func copyFile(b Backend, srcPath, relPath string, now time.Time, verify bool) error {
	if err := b.MkdirAll(path.Dir(relPath)); err != nil {
		return err
	}

	attempt := func() error {
		src, err := os.Open(srcPath)
		if err != nil {
			return err
		}
		defer src.Close()
		srcInfo, err := src.Stat()
		if err != nil {
			return err
		}

		tmp := path.Join(path.Dir(relPath), "."+path.Base(relPath)+tmpSuffix)
		dst, err := b.OpenWrite(tmp)
		if err != nil {
			return err
		}
		cleanup := func() { dst.Close(); _ = b.Remove(tmp) }

		hasher := sha256.New()
		var w io.Writer = dst
		if verify {
			w = io.MultiWriter(dst, hasher)
		}
		if _, err := io.Copy(w, src); err != nil {
			cleanup()
			return fmt.Errorf("copying %s: %w", relPath, err)
		}
		if err := dst.Sync(); err != nil {
			cleanup()
			return err
		}
		if err := dst.Close(); err != nil {
			_ = b.Remove(tmp)
			return err
		}
		// Coarse-timestamp targets can't always represent the exact mtime;
		// the compare tolerance covers the gap.
		_ = b.Chtimes(tmp, time.Now(), srcInfo.ModTime())

		if verify {
			if err := verifyFile(b, tmp, hasher.Sum(nil)); err != nil {
				_ = b.Remove(tmp)
				return err
			}
		}

		if _, err := b.Stat(relPath); err == nil {
			if err := keepVersion(b, relPath, now); err != nil {
				_ = b.Remove(tmp)
				return err
			}
		}
		return b.Rename(tmp, relPath)
	}

	err := attempt()
	if err != nil && verify {
		// One retry: a transient network hiccup shouldn't cost the file.
		err = attempt()
	}
	return err
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
