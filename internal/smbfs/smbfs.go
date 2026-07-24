// SPDX-License-Identifier: MIT

// Package smbfs implements localmirror.Backend over an SMB network share, so
// any drive reachable on the LAN — NAS boxes, router USB drives, folders
// shared by other computers — works as a backup target with no OS mounting
// and no admin rights.
package smbfs

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"os"
	"path"
	"strings"
	"sync"
	"time"

	smb2 "github.com/cloudsoda/go-smb2"

	"github.com/phil9922/backup-maker/internal/localmirror"
)

const (
	dialTimeout = 5 * time.Second
	// opTimeout bounds metadata operations so a dead connection (wifi drop,
	// server power-off) surfaces as an error instead of hanging the engine.
	// Bulk file reads/writes run on open handles and rely on TCP keepalive.
	opTimeout = 60 * time.Second
)

// FS is a lazily-connected SMB backend. One FS per mirror engine: a single
// session, re-dialed transparently after any network error. No keepalive
// traffic — the engine's offline poll is the natural reconnect cadence.
type FS struct {
	addr    string // host:port
	share   string
	subpath string // slash path inside the share ("" = share root)
	user    string
	pass    string

	mu   sync.Mutex
	sess *smb2.Session
	sh   *smb2.Share
}

// New builds an SMB backend for url ("//host[:port]/share[/subpath]").
func New(url, username, password string) (*FS, error) {
	host, port, share, subpath, err := Parse(url)
	if err != nil {
		return nil, err
	}
	return &FS{
		addr:    net.JoinHostPort(host, port),
		share:   share,
		subpath: subpath,
		user:    username,
		pass:    password,
	}, nil
}

// Parse splits //host[:port]/share[/subpath]. Also accepts backslashes and
// an smb:// prefix, since users paste addresses in all shapes.
func Parse(url string) (host, port, share, subpath string, err error) {
	s := strings.ReplaceAll(url, `\`, "/")
	s = strings.TrimPrefix(s, "smb://")
	s = strings.TrimPrefix(s, "//")
	parts := strings.SplitN(strings.Trim(s, "/"), "/", 3)
	if len(parts) < 2 || parts[0] == "" || parts[1] == "" {
		return "", "", "", "", fmt.Errorf("share address must look like //host/share, got %q", url)
	}
	host = parts[0]
	port = "445"
	if h, p, splitErr := net.SplitHostPort(host); splitErr == nil {
		host, port = h, p
	}
	share = parts[1]
	if len(parts) == 3 {
		subpath = parts[2]
	}
	return host, port, share, subpath, nil
}

// URL reconstructs the canonical //host:port/share/subpath form.
func (f *FS) URL() string {
	u := "//" + f.addr + "/" + f.share
	if f.subpath != "" {
		u += "/" + f.subpath
	}
	return u
}

// conn returns a live share mount, dialing if needed.
func (f *FS) conn() (*smb2.Share, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.sh != nil {
		return f.sh, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), dialTimeout)
	defer cancel()
	d := &smb2.Dialer{
		Initiator: &smb2.NTLMInitiator{User: f.user, Password: f.pass},
	}
	sess, err := d.Dial(ctx, f.addr)
	if err != nil {
		return nil, fmt.Errorf("connecting to %s: %w", f.addr, err)
	}
	sh, err := sess.Mount(f.share)
	if err != nil {
		_ = sess.Logoff()
		return nil, fmt.Errorf("opening share %q on %s: %w", f.share, f.addr, err)
	}
	f.sess, f.sh = sess, sh
	return sh, nil
}

// connCtx returns a context-bound share for one metadata operation.
func (f *FS) connCtx() (*smb2.Share, context.CancelFunc, error) {
	sh, err := f.conn()
	if err != nil {
		return nil, nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), opTimeout)
	return sh.WithContext(ctx), cancel, nil
}

// reset drops the session after a network-class error so the next op
// re-dials. Teardown is context-bound: quirky servers must not hang us.
func (f *FS) reset() {
	f.mu.Lock()
	defer f.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if f.sh != nil {
		_ = f.sh.WithContext(ctx).Umount()
		f.sh = nil
	}
	if f.sess != nil {
		_ = f.sess.WithContext(ctx).Logoff()
		f.sess = nil
	}
}

// NT status codes signalling "does not exist" ([MS-ERREF]).
const (
	ntObjectNameNotFound = 0xC0000034
	ntObjectPathNotFound = 0xC000003A
	ntNoSuchFile         = 0xC000000F
	// The server telling us its volume is full. Mapped to a distinct error so
	// the reclaimer only ever fires on a genuinely full destination.
	ntDiskFull      = 0xC000007F
	ntQuotaExceeded = 0xC0000044
)

// opErr normalizes an op failure: connection-class errors reset the session,
// and not-found responses are wrapped so errors.Is(err, fs.ErrNotExist)
// works — the mirror engine depends on that.
func (f *FS) opErr(err error) error {
	if err == nil {
		return nil
	}
	var re *smb2.ResponseError
	if errors.As(err, &re) {
		switch re.Code {
		case ntObjectNameNotFound, ntObjectPathNotFound, ntNoSuchFile:
			return fmt.Errorf("%w: %w", fs.ErrNotExist, err)
		case ntDiskFull, ntQuotaExceeded:
			return fmt.Errorf("%w: %w", localmirror.ErrNoSpace, err)
		}
		return err // a server answer, not a connection problem
	}
	if isConnError(err) {
		f.reset()
	}
	return err
}

func isConnError(err error) bool {
	var ne net.Error
	if errors.As(err, &ne) {
		return true
	}
	s := err.Error()
	return strings.Contains(s, "connection") || strings.Contains(s, "broken pipe") ||
		strings.Contains(s, "EOF") || strings.Contains(s, "session")
}

// p converts a slash-relative backend path to the share-relative backslash
// form go-smb2 expects.
func (f *FS) p(rel string) string {
	full := path.Join(f.subpath, rel)
	return strings.ReplaceAll(strings.Trim(full, "/"), "/", `\`)
}

// --- localmirror.Backend ---

func (f *FS) Stat(rel string) (fs.FileInfo, error) {
	sh, cancel, err := f.connCtx()
	if err != nil {
		return nil, err
	}
	defer cancel()
	fi, err := sh.Stat(f.p(rel))
	return fi, f.opErr(err)
}

// OpenRead returns a reader that reads exactly the file's current size via
// ReadAt, signalling io.EOF itself. This sidesteps server EOF-error quirks
// (some SMB stacks send malformed STATUS_END_OF_FILE responses) and never
// depends on a trailing zero-byte read.
func (f *FS) OpenRead(rel string) (io.ReadCloser, error) {
	sh, err := f.conn()
	if err != nil {
		return nil, err
	}
	file, err := sh.Open(f.p(rel))
	if err != nil {
		return nil, f.opErr(err)
	}
	fi, err := file.Stat()
	if err != nil {
		file.Close()
		return nil, f.opErr(err)
	}
	return &sizedReader{f: file, size: fi.Size()}, nil
}

type sizedReader struct {
	f    *smb2.File
	size int64
	off  int64
}

func (r *sizedReader) Read(p []byte) (int, error) {
	if r.off >= r.size {
		return 0, io.EOF
	}
	if rest := r.size - r.off; int64(len(p)) > rest {
		p = p[:rest]
	}
	n, err := r.f.ReadAt(p, r.off)
	r.off += int64(n)
	if err == io.EOF && r.off >= r.size {
		err = nil // final chunk fully read; next call reports EOF
	}
	return n, err
}

func (r *sizedReader) Close() error { return r.f.Close() }

func (f *FS) OpenWrite(rel string) (localmirror.WFile, error) {
	sh, err := f.conn()
	if err != nil {
		return nil, err
	}
	file, err := sh.OpenFile(f.p(rel), os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return nil, f.opErr(err)
	}
	return file, nil
}

func (f *FS) Rename(oldRel, newRel string) error {
	sh, cancel, err := f.connCtx()
	if err != nil {
		return err
	}
	defer cancel()
	err = sh.Rename(f.p(oldRel), f.p(newRel))
	if err == nil {
		return nil
	}
	if isConnError(err) {
		return f.opErr(err)
	}
	// SMB rename does not replace an existing destination; emulate POSIX by
	// removing it first. Safe: callers version away anything valuable before
	// renaming over it.
	if rmErr := sh.Remove(f.p(newRel)); rmErr == nil {
		return f.opErr(sh.Rename(f.p(oldRel), f.p(newRel)))
	}
	return f.opErr(err)
}

func (f *FS) Remove(rel string) error {
	sh, cancel, err := f.connCtx()
	if err != nil {
		return err
	}
	defer cancel()
	return f.opErr(sh.Remove(f.p(rel)))
}

func (f *FS) RemoveAll(rel string) error {
	sh, cancel, err := f.connCtx()
	if err != nil {
		return err
	}
	defer cancel()
	return f.opErr(sh.RemoveAll(f.p(rel)))
}

func (f *FS) MkdirAll(rel string) error {
	sh, cancel, err := f.connCtx()
	if err != nil {
		return err
	}
	defer cancel()
	if f.p(rel) == "" {
		return nil // share root always exists
	}
	return f.opErr(sh.MkdirAll(f.p(rel), 0o755))
}

func (f *FS) Chtimes(rel string, atime, mtime time.Time) error {
	sh, cancel, err := f.connCtx()
	if err != nil {
		return err
	}
	defer cancel()
	return f.opErr(sh.Chtimes(f.p(rel), atime, mtime))
}

func (f *FS) WalkDir(root string, fn fs.WalkDirFunc) error {
	sh, err := f.conn()
	if err != nil {
		// Report the error through fn like filepath.WalkDir does for an
		// unreadable root, so reconcile treats it as an interrupted sync.
		return fn(root, nil, err)
	}
	base := f.p(root)
	if base == "" {
		base = "."
	}
	dirFS := sh.DirFS(base)
	walkErr := fs.WalkDir(dirFS, ".", func(p string, d fs.DirEntry, err error) error {
		rel := root
		if p != "." {
			rel = path.Join(root, strings.ReplaceAll(p, `\`, "/"))
		}
		return fn(rel, d, err)
	})
	return f.opErr(walkErr)
}

// ReadFile goes through OpenRead's size-bounded reader (not the library's
// ReadFile) so it never depends on server EOF-error formatting.
func (f *FS) ReadFile(rel string) ([]byte, error) {
	r, err := f.OpenRead(rel)
	if err != nil {
		return nil, err
	}
	defer r.Close()
	data, err := io.ReadAll(r)
	return data, f.opErr(err)
}

func (f *FS) WriteFile(rel string, data []byte) error {
	sh, cancel, err := f.connCtx()
	if err != nil {
		return err
	}
	defer cancel()
	return f.opErr(sh.WriteFile(f.p(rel), data, 0o644))
}

func (f *FS) Close() error {
	f.reset()
	return nil
}

// --- setup helpers ---

// TestConnection dials, mounts, and stats the share root (creating the
// subpath if needed) — used by add-target before anything is saved.
func TestConnection(url, username, password string) error {
	f, err := New(url, username, password)
	if err != nil {
		return err
	}
	defer f.Close()
	if err := f.MkdirAll(""); err != nil {
		return err
	}
	if f.subpath != "" {
		sh, err := f.conn()
		if err != nil {
			return err
		}
		if err := sh.MkdirAll(strings.ReplaceAll(f.subpath, "/", `\`), 0o755); err != nil {
			return fmt.Errorf("creating %s: %w", f.subpath, err)
		}
	}
	_, err = f.Stat("")
	return err
}

// ListShares connects to addr (host or host:port) and lists its non-admin
// share names. Empty username tries an anonymous/guest session.
func ListShares(ctx context.Context, addr, username, password string) ([]string, error) {
	if _, _, err := net.SplitHostPort(addr); err != nil {
		addr = net.JoinHostPort(addr, "445")
	}
	d := &smb2.Dialer{Initiator: &smb2.NTLMInitiator{User: username, Password: password}}
	sess, err := d.Dial(ctx, addr)
	if err != nil {
		return nil, err
	}
	defer sess.Logoff()
	names, err := sess.ListSharenames()
	if err != nil {
		return nil, err
	}
	var out []string
	for _, n := range names {
		if !strings.HasSuffix(n, "$") { // skip administrative shares
			out = append(out, n)
		}
	}
	return out, nil
}

// Usage reports the space available to this user on the share, satisfying
// localmirror.SpaceReporter.
//
// It uses the caller-available figure rather than the volume-wide free figure,
// so a per-user quota is respected: writing until the volume is empty would
// fail long before that on a quota'd share.
func (f *FS) Usage() (avail, total uint64, err error) {
	sh, cancel, err := f.connCtx()
	if err != nil {
		return 0, 0, err
	}
	defer cancel()
	st, err := sh.Statfs(f.p(""))
	if err != nil {
		return 0, 0, f.opErr(err)
	}
	block := st.BlockSize()
	return st.AvailableBlockCount() * block, st.TotalBlockCount() * block, nil
}
