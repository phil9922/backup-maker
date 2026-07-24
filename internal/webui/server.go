// SPDX-License-Identifier: MIT

// Package webui serves the localhost dashboard and the JSON API used by both
// the browser UI and the CLI. It binds to 127.0.0.1 only — never a LAN
// interface.
package webui

import (
	"context"
	"crypto/subtle"
	"embed"
	"fmt"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/phil9922/backup-maker/internal/config"
)

//go:embed static
var staticFS embed.FS

const sessionCookie = "backupmaker_session"

// Actions are daemon-provided callbacks behind the API (kept as functions to
// avoid an import cycle between webui and daemon).
type Actions struct {
	// Scan looks for SMB servers on the LAN — on demand only.
	Scan func(ctx context.Context) (any, error)
	// AddShare creates a network-drive target.
	AddShare func(req AddShareRequest) error
	// Wake sends a Wake-on-LAN packet to one target, bypassing the
	// background rate limit. Errors when the target can't be woken.
	Wake func(target string) error
	// Machines lists computers that could hold backups. scan controls whether
	// the LAN is probed — never implicitly.
	Machines func(ctx context.Context, scan bool) (any, error)
	// Storage lists what can be selected on one machine.
	Storage func(ctx context.Context, req StorageRequest) (any, error)
	// CreateBackup wires one folder to several destinations atomically.
	CreateBackup func(req BackupRequest) (any, error)
	// RemoveFolder and RemoveTarget only edit configuration; backup data on
	// the target is never deleted.
	RemoveFolder func(id string) error
	RemoveTarget func(name string) error
	// AddArchive schedules an encrypted snapshot job.
	AddArchive func(req ArchiveRequest) error
	// CompleteSetup marks first-run setup finished or skipped.
	CompleteSetup func() error
}

type StorageRequest struct {
	Machine  string `json:"machine"`
	Username string `json:"username,omitempty"`
	Password string `json:"password,omitempty"`
}

// BackupRequest mirrors setup.BackupRequest; kept as its own type so webui
// doesn't force an import cycle and the JSON contract stays explicit.
type BackupRequest struct {
	// FolderID gives a second kind of backup to a folder that is already
	// protected, instead of adding a new one.
	FolderID    string   `json:"folder_id,omitempty"`
	Path        string   `json:"path"`
	Label       string   `json:"label,omitempty"`
	ExtraIgnore []string `json:"extra_ignore,omitempty"`
	// Mode is "incremental" (a continuous mirror) or "timed" (scheduled
	// encrypted snapshots and no live copy).
	Mode         string        `json:"mode,omitempty"`
	Destinations []Destination `json:"destinations"`
	// Archive is required for a timed backup — the schedule is its only
	// protection — and optional otherwise.
	Archive *ArchiveSpec `json:"archive,omitempty"`
}

type ArchiveSpec struct {
	Name     string `json:"name"`
	Every    string `json:"every"`
	Keep     int    `json:"keep,omitempty"`
	Password string `json:"password"`
	// IncludeEverything seals node_modules, build output and caches into this
	// snapshot only, leaving the live mirror lean.
	IncludeEverything bool `json:"include_everything,omitempty"`
}

type Destination struct {
	ExistingTarget string `json:"existing_target,omitempty"`
	Name           string `json:"name,omitempty"`
	Path           string `json:"path,omitempty"`
	URL            string `json:"url,omitempty"`
	Username       string `json:"username,omitempty"`
	Password       string `json:"password,omitempty"`
	DeviceID       string `json:"device_id,omitempty"`
	MAC            string `json:"mac,omitempty"`
	NoVerify       bool   `json:"no_verify,omitempty"`
}

type ArchiveRequest struct {
	Name     string   `json:"name"`
	Folders  []string `json:"folders,omitempty"`
	Every    string   `json:"every"`
	Target   string   `json:"target"`
	Keep     int      `json:"keep,omitempty"`
	Password string   `json:"password"`
}

type WakeRequest struct {
	Target string `json:"target"`
}

type AddShareRequest struct {
	URL      string `json:"url"`
	Username string `json:"username"`
	Password string `json:"password"`
	Name     string `json:"name"`
	Verify   bool   `json:"verify"`
}

type Server struct {
	// mux is shared with the optional LAN listener, which wraps it in an
	// allow-list. Sharing the routes means the read-only view can never drift
	// out of sync with the real dashboard.
	mux     *http.ServeMux
	cfg     *config.Config
	state   *config.State
	log     *slog.Logger
	http    *http.Server
	ln      net.Listener
	status  func() any
	actions Actions
}

func New(cfg *config.Config, state *config.State, log *slog.Logger, statusFn func() any, actions Actions) (*Server, error) {
	s := &Server{cfg: cfg, state: state, log: log, status: statusFn, actions: actions}

	static, err := fs.Sub(staticFS, "static")
	if err != nil {
		return nil, err
	}

	mux := http.NewServeMux()
	mux.Handle("GET /", http.FileServerFS(static))
	mux.HandleFunc("GET /auth", s.handleAuth)
	mux.HandleFunc("GET /api/ping", s.requireToken(s.handlePing))
	mux.HandleFunc("GET /api/status", s.requireToken(s.handleStatus))
	mux.HandleFunc("POST /api/scan", s.requireToken(s.handleScan))
	mux.HandleFunc("POST /api/targets/share", s.requireToken(s.handleAddShare))
	mux.HandleFunc("POST /api/wake", s.requireToken(s.handleWake))
	mux.HandleFunc("GET /api/browse", s.requireToken(s.handleBrowse))
	mux.HandleFunc("GET /api/machines", s.requireToken(s.handleMachines))
	mux.HandleFunc("POST /api/machines/storage", s.requireToken(s.handleStorage))
	mux.HandleFunc("POST /api/backups", s.requireToken(s.handleCreateBackup))
	mux.HandleFunc("DELETE /api/folders/{id}", s.requireToken(s.handleRemoveFolder))
	mux.HandleFunc("DELETE /api/targets/{name}", s.requireToken(s.handleRemoveTarget))
	mux.HandleFunc("POST /api/archives", s.requireToken(s.handleAddArchive))
	mux.HandleFunc("POST /api/setup/complete", s.requireToken(s.handleCompleteSetup))

	addr := fmt.Sprintf("127.0.0.1:%d", cfg.General.DashboardPort)
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("binding dashboard on %s: %w", addr, err)
	}
	s.ln = ln
	s.mux = mux
	s.http = &http.Server{Handler: loopbackOnly(mux), ReadHeaderTimeout: 10 * time.Second}
	return s, nil
}

// loopbackOnly rejects requests whose Host header isn't loopback.
//
// The listener already binds 127.0.0.1, so this is defence in depth: it makes
// the loopback intent explicit and blocks DNS-rebinding attempts, where a
// hostile page re-resolves its own domain to 127.0.0.1 to talk to local
// services. (The session cookie is scoped to 127.0.0.1 and marked
// SameSite=Strict, so such a request would fail authentication anyway — this
// stops it a step earlier.)
func loopbackOnly(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host := r.Host
		if h, _, err := net.SplitHostPort(host); err == nil {
			host = h
		}
		switch host {
		case "127.0.0.1", "localhost", "::1", "[::1]", "":
			next.ServeHTTP(w, r)
		default:
			http.Error(w, "this dashboard only answers on localhost", http.StatusForbidden)
		}
	})
}

func (s *Server) Serve() error {
	err := s.http.Serve(s.ln)
	if err == http.ErrServerClosed {
		return nil
	}
	return err
}

func (s *Server) Shutdown() {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = s.http.Shutdown(ctx)
}

// URL returns the dashboard base URL.
func (s *Server) URL() string {
	return fmt.Sprintf("http://127.0.0.1:%d", s.cfg.General.DashboardPort)
}

// AuthURL returns a URL that logs the browser in via one redirect. Only ever
// printed locally or passed to the local browser opener.
func (s *Server) AuthURL() string {
	return s.URL() + "/auth?token=" + s.state.IPCToken
}

func (s *Server) tokenOK(tok string) bool {
	return tok != "" && subtle.ConstantTimeCompare([]byte(tok), []byte(s.state.IPCToken)) == 1
}

// handleAuth exchanges the token (from a locally-opened URL) for an HttpOnly
// session cookie, so the dashboard JS never handles the token itself.
func (s *Server) handleAuth(w http.ResponseWriter, r *http.Request) {
	if !s.tokenOK(r.URL.Query().Get("token")) {
		http.Error(w, "invalid token", http.StatusForbidden)
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookie,
		Value:    s.state.IPCToken,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
	})
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

// requireToken authorizes a request via Bearer token (CLI) or session cookie
// (browser). Binding to loopback keeps the LAN out; the token keeps other
// local users on multi-user machines out.
func (s *Server) requireToken(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if bearer, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer "); ok && s.tokenOK(bearer) {
			next(w, r)
			return
		}
		if c, err := r.Cookie(sessionCookie); err == nil && s.tokenOK(c.Value) {
			next(w, r)
			return
		}
		http.Error(w, "unauthorized", http.StatusUnauthorized)
	}
}
