// SPDX-License-Identifier: MIT

package localmirror

import (
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"github.com/phil9922/backup-maker/internal/diskspace"
)

// Backend is the destination-side filesystem of a mirror target. All paths
// are slash-separated and relative to the target root (the directory holding
// the marker file). The source side always uses local os I/O + fsnotify —
// only where the backup lands is abstracted (local drive, SMB share, ...).
type Backend interface {
	Stat(path string) (fs.FileInfo, error)
	OpenRead(path string) (io.ReadCloser, error)
	// OpenWrite creates or truncates path for writing.
	OpenWrite(path string) (WFile, error)
	// Rename moves oldpath to newpath, replacing any existing newpath.
	Rename(oldpath, newpath string) error
	Remove(path string) error
	RemoveAll(path string) error
	MkdirAll(path string) error
	Chtimes(path string, atime, mtime time.Time) error
	// WalkDir walks the tree rooted at root ("" = target root) calling fn
	// with slash-separated paths relative to the target root.
	WalkDir(root string, fn fs.WalkDirFunc) error
	ReadFile(path string) ([]byte, error)
	WriteFile(path string, data []byte) error
	Close() error
}

// WFile is a writable file that can be flushed to stable storage.
type WFile interface {
	io.WriteCloser
	Sync() error
}

// SpaceReporter is implemented by backends that can say how full their storage
// is. It is deliberately separate from Backend: a backend that can't answer
// should disable space reclaiming, not fail to exist. Callers type-assert.
type SpaceReporter interface {
	// Usage returns bytes available to this user and the total capacity.
	Usage() (avail, total uint64, err error)
}

// ErrNoSpace reports a write that failed because the destination is full.
//
// It has to be distinguishable from every other write error: reclaiming space
// deletes backup history, so it must only ever be triggered by a genuinely
// full destination, never by a permission problem or a dropped connection.
var ErrNoSpace = errors.New("destination is full")

func (l *localFS) Usage() (uint64, uint64, error) { return diskspace.Usage(l.root) }

// localFS implements Backend on a locally mounted directory (internal HDD
// partition, SD card, USB stick, external disk).
type localFS struct {
	root string
}

// NewLocalFS returns a Backend rooted at an OS directory path.
func NewLocalFS(root string) Backend {
	return &localFS{root: root}
}

func (l *localFS) abs(p string) string {
	return filepath.Join(l.root, filepath.FromSlash(p))
}

func (l *localFS) Stat(p string) (fs.FileInfo, error) { return os.Stat(l.abs(p)) }
func (l *localFS) Remove(p string) error              { return os.Remove(l.abs(p)) }
func (l *localFS) RemoveAll(p string) error           { return os.RemoveAll(l.abs(p)) }
func (l *localFS) MkdirAll(p string) error            { return os.MkdirAll(l.abs(p), 0o755) }
func (l *localFS) ReadFile(p string) ([]byte, error)  { return os.ReadFile(l.abs(p)) }
func (l *localFS) WriteFile(p string, d []byte) error { return os.WriteFile(l.abs(p), d, 0o644) }
func (l *localFS) Close() error                       { return nil }

func (l *localFS) OpenRead(p string) (io.ReadCloser, error) {
	return os.Open(l.abs(p))
}

func (l *localFS) OpenWrite(p string) (WFile, error) {
	return os.OpenFile(l.abs(p), os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
}

func (l *localFS) Rename(oldp, newp string) error {
	return os.Rename(l.abs(oldp), l.abs(newp))
}

func (l *localFS) Chtimes(p string, atime, mtime time.Time) error {
	return os.Chtimes(l.abs(p), atime, mtime)
}

func (l *localFS) WalkDir(root string, fn fs.WalkDirFunc) error {
	base := l.abs(root)
	return filepath.WalkDir(base, func(p string, d fs.DirEntry, err error) error {
		rel, rerr := filepath.Rel(l.root, p)
		if rerr != nil {
			rel = p
		}
		return fn(filepath.ToSlash(rel), d, err)
	})
}

// IsNoSpace reports whether err means the destination ran out of room —
// whether that came from the local filesystem or from an SMB server.
func IsNoSpace(err error) bool {
	return err != nil && (errors.Is(err, ErrNoSpace) || isSystemNoSpace(err))
}
