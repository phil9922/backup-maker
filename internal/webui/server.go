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
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/phil9922/backup-maker/internal/config"
	"github.com/phil9922/backup-maker/internal/version"
)

//go:embed static
var staticFS embed.FS

const sessionCookie = "backupmaker_session"

// sessionLifetime is how long a signed-in browser stays signed in. It is
// refreshed on every authenticated request, so in practice this is "how long
// the dashboard can sit unopened before you have to run `backup-maker web`
// again" — and it is deliberately long enough that nobody ever hits it.
//
// A shorter window would look more careful and buy nothing. The cookie's value
// IS the IPC token, which never rotates and sits in state.json on disk; anyone
// who can read the browser profile can read that file too, as the same user on
// the same machine. Expiring the cookie therefore raises no attacker's cost —
// it only creates a day when a bookmark silently stops working, which pushes
// people toward bookmarking the ?token=... URL instead. That is strictly worse:
// a live credential in a syncable bookmark.
const sessionLifetime = 365 * 24 * time.Hour

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
	// AcceptPair approves a machine that is waiting to back up here. Only a
	// device already asking to pair can be approved.
	AcceptPair func(deviceID string) error
	// DeviceID is this machine's own identity, for the pairing QR code. It
	// errors while the (lazy) sync engine isn't running.
	DeviceID func() (string, error)
	// RevertFolder discards changes made on THIS machine to a backup it holds
	// for another machine. Destructive: files that exist only here are deleted.
	// Only a folder this machine receives may be named; the daemon checks.
	RevertFolder func(folderID string) error
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
	// SetFolderIgnores replaces one folder's exclude patterns. Excluding
	// something stops it being copied; it never deletes an existing backup.
	SetFolderIgnores func(id string, extraIgnore []string, noDefaultIgnores bool) error
	// AddArchive schedules an encrypted snapshot job.
	AddArchive func(req ArchiveRequest) error
	// CompleteSetup marks first-run setup finished or skipped.
	CompleteSetup func() error
	// AdoptAllowed reports whether this machine may adopt an existing
	// destination (nothing configured yet). Gates every adopt route.
	AdoptAllowed func() error
	// AdoptScan lists attached drives that carry an adoption manifest.
	AdoptScan func() (any, error)
	// AdoptInspect reads a destination's manifest without writing anything.
	AdoptInspect func(req AdoptSourceRequest) (any, error)
	// AdoptTestShare verifies share credentials with a real connection.
	AdoptTestShare func(req TestShareRequest) error
	// Adopt rebuilds this machine's configuration from a destination.
	Adopt func(req AdoptRequest) (any, error)
	// SetArchivePassword stores the zip password for an existing snapshot job.
	SetArchivePassword func(name, password string) error
	// SetSettings changes [general]: which alerts are wanted, and whether the
	// read-only network view runs. Deliberately narrow — machine name, ports
	// and the receive root are not reachable from the browser, because a
	// mis-click on any of them breaks a working setup in a way this panel
	// could not walk back.
	SetSettings func(req SettingsRequest) error
	// LANGate decides which devices may read the read-only network view. nil
	// leaves the view open to everyone on the network, which is what it was
	// before the setting existed.
	LANGate *LANGate
	// ApproveLANDevice and ForgetLANDevice manage that list from the
	// dashboard. Loopback-only, like every other mutating route.
	ApproveLANDevice func(code string) error
	ForgetLANDevice  func(code string) error
	// TestAlert delivers one notification by the named method and reports
	// whether it actually arrived. Synchronous on purpose — the answer is the
	// entire value of the button.
	TestAlert func(ctx context.Context, method string) error
}

// SettingsRequest is a partial update: a field left out is left alone. Pointers
// rather than values so that "unchecked" and "not sent" stay distinguishable —
// with plain bools, a client that sent only the field it changed would switch
// off every alert it omitted.
type SettingsRequest struct {
	DesktopAlerts       *bool `json:"desktop_alerts,omitempty"`
	BackupsStopped      *bool `json:"alert_backups_stopped,omitempty"`
	SnapshotFailed      *bool `json:"alert_snapshot_failed,omitempty"`
	UnrecognisedStorage *bool `json:"alert_unrecognised_storage,omitempty"`
	PairRequests        *bool `json:"alert_pair_requests,omitempty"`
	LANView             *bool `json:"lan_view,omitempty"`
	// LANViewAccess is "all" or "approved". A string rather than a bool
	// because it is a choice between two named things, and the panel shows it
	// as two options with names.
	LANViewAccess string `json:"lan_view_access,omitempty"`
	// Webhook delivery. Independent of DesktopAlerts, not an alternative to
	// it: both can be on, and a user with a phone relay usually wants both.
	WebhookEnabled *bool `json:"webhook_enabled,omitempty"`
	WebhookMinimal *bool `json:"webhook_minimal,omitempty"`
	// WebhookURL is sent only when it is being changed. Never echoed back in
	// status — it is a credential.
	WebhookURL *string `json:"webhook_url,omitempty"`
	// ntfy delivery. Independent of both of the above, for the same reason.
	NtfyEnabled *bool `json:"ntfy_enabled,omitempty"`
	NtfyMinimal *bool `json:"ntfy_minimal,omitempty"`
	// NtfyTopicURL and NtfyToken are sent only when being changed, and never
	// echoed back. Two fields rather than one because they are saved
	// separately: the browser is never told the token, so a combined save would
	// clear it every time the topic was edited.
	NtfyTopicURL *string `json:"ntfy_topic_url,omitempty"`
	NtfyToken    *string `json:"ntfy_token,omitempty"`
}

// AdoptSourceRequest locates the destination to adopt from: a local path, or
// a share URL plus credentials.
type AdoptSourceRequest struct {
	Path     string `json:"path,omitempty"`
	URL      string `json:"url,omitempty"`
	Username string `json:"username,omitempty"`
	Password string `json:"password,omitempty"`
}

type TestShareRequest struct {
	URL      string `json:"url"`
	Username string `json:"username,omitempty"`
	Password string `json:"password,omitempty"`
}

// AdoptRequest mirrors setup.AdoptDecisions plus the source to re-read the
// manifest from. Passwords are keyed by name; PRESENCE of a key is what
// stores a credential (an empty value is a valid guest password).
type AdoptRequest struct {
	Source            AdoptSourceRequest `json:"source"`
	ContinueAsMachine bool               `json:"continue_as_machine"`
	NewMachineName    string             `json:"new_machine_name,omitempty"`
	PathRemap         map[string]string  `json:"path_remap,omitempty"`
	SharePasswords    map[string]string  `json:"share_passwords,omitempty"`
	ArchivePasswords  map[string]string  `json:"archive_passwords,omitempty"`
}

type ArchivePasswordRequest struct {
	Password string `json:"password"`
}

// FolderIgnoresRequest replaces a folder's excludes wholesale: the list sent is
// the list stored. NoDefaultIgnores must be sent back as it was read, or a
// folder that opted out of the standard exclusions would silently opt back in.
type FolderIgnoresRequest struct {
	Ignores          []string `json:"ignores"`
	NoDefaultIgnores bool     `json:"no_default_ignores"`
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

// PairAcceptRequest names the machine to approve by its full device ID — never
// a prefix, so the ID that gets approved is the one that was read and checked.
type PairAcceptRequest struct {
	DeviceID string `json:"device_id"`
}

// ReceiveRevertRequest names the received backup to restore from its sender.
type ReceiveRevertRequest struct {
	FolderID string `json:"folder_id"`
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
	// lanGate decides which devices may read the network view. Taken from
	// actions so the daemon owns the device list; nil means open to everyone.
	lanGate *LANGate
	// done is closed on Shutdown so the event streams end promptly. They never
	// go idle on their own, and http.Server.Shutdown waits for idle.
	done     chan struct{}
	doneOnce sync.Once
}

func New(cfg *config.Config, state *config.State, log *slog.Logger, statusFn func() any, actions Actions) (*Server, error) {
	s := &Server{cfg: cfg, state: state, log: log, status: statusFn, actions: actions, lanGate: actions.LANGate, done: make(chan struct{})}

	static, err := fs.Sub(staticFS, "static")
	if err != nil {
		return nil, err
	}

	mux := http.NewServeMux()
	mux.Handle("GET /", staticAssets(static))
	mux.HandleFunc("GET /auth", s.handleAuth)
	mux.HandleFunc("GET /api/ping", s.requireToken(s.handlePing))
	mux.HandleFunc("GET /api/status", s.requireToken(s.handleStatus))
	mux.HandleFunc("GET /api/events", s.requireToken(s.handleEvents))
	mux.HandleFunc("POST /api/scan", s.requireToken(s.handleScan))
	mux.HandleFunc("POST /api/targets/share", s.requireToken(s.handleAddShare))
	mux.HandleFunc("POST /api/wake", s.requireToken(s.handleWake))
	mux.HandleFunc("POST /api/pair/accept", s.requireToken(s.handleAcceptPair))
	mux.HandleFunc("GET /api/pair/qr", s.requireToken(s.handlePairQR))
	mux.HandleFunc("POST /api/receive/revert", s.requireToken(s.handleRevertFolder))
	mux.HandleFunc("GET /api/browse", s.requireToken(s.handleBrowse))
	mux.HandleFunc("GET /api/machines", s.requireToken(s.handleMachines))
	mux.HandleFunc("POST /api/machines/storage", s.requireToken(s.handleStorage))
	mux.HandleFunc("POST /api/backups", s.requireToken(s.handleCreateBackup))
	mux.HandleFunc("DELETE /api/folders/{id}", s.requireToken(s.handleRemoveFolder))
	mux.HandleFunc("POST /api/folders/{id}/ignores", s.requireToken(s.handleSetFolderIgnores))
	mux.HandleFunc("DELETE /api/targets/{name}", s.requireToken(s.handleRemoveTarget))
	mux.HandleFunc("POST /api/archives", s.requireToken(s.handleAddArchive))
	mux.HandleFunc("POST /api/setup/complete", s.requireToken(s.handleCompleteSetup))
	mux.HandleFunc("GET /api/adopt/scan", s.requireToken(s.handleAdoptScan))
	mux.HandleFunc("POST /api/adopt/inspect", s.requireToken(s.handleAdoptInspect))
	mux.HandleFunc("POST /api/adopt/test-share", s.requireToken(s.handleAdoptTestShare))
	mux.HandleFunc("POST /api/adopt", s.requireToken(s.handleAdopt))
	mux.HandleFunc("POST /api/archives/{name}/password", s.requireToken(s.handleArchivePassword))
	mux.HandleFunc("POST /api/settings", s.requireToken(s.handleSetSettings))
	mux.HandleFunc("POST /api/alerts/test", s.requireToken(s.handleTestAlert))
	mux.HandleFunc("POST /api/lan-devices/{code}/approve", s.requireToken(s.handleApproveLANDevice))
	mux.HandleFunc("DELETE /api/lan-devices/{code}", s.requireToken(s.handleForgetLANDevice))

	addr := fmt.Sprintf("127.0.0.1:%d", cfg.General.DashboardPort)
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("binding dashboard on %s: %w", addr, err)
	}
	s.ln = ln
	s.mux = mux
	s.http = &http.Server{Handler: loopbackOnly(fromTheDashboardOnly(mux)), ReadHeaderTimeout: 10 * time.Second}
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
		// An empty Host is HTTP/1.0, not a name that resolves somewhere else.
		if host == "" || loopbackHost(host) {
			next.ServeHTTP(w, r)
			return
		}
		http.Error(w, "this dashboard only answers on localhost", http.StatusForbidden)
	})
}

// loopbackHost reports whether a hostname (no port) names this machine. Shared
// with the Origin check so the two can't drift apart.
func loopbackHost(host string) bool {
	switch host {
	case "127.0.0.1", "localhost", "::1", "[::1]":
		return true
	}
	return false
}

// MutationHeader must be sent on every request that changes something. Any
// non-empty value will do: what matters is that it was sent at all.
const MutationHeader = "X-Backup-Maker"

// fromTheDashboardOnly refuses state-changing requests that a browser made from
// any page other than the dashboard itself.
//
// Loopback is not an origin boundary. A browser has no registrable domain for a
// bare IP, so EVERY PORT ON 127.0.0.1 COUNTS AS THE SAME SITE: a page served by
// any other local process — another user's on a multi-user machine — can POST
// here and the browser attaches the session cookie. SameSite=Strict does not
// stop it; such a request is reported as Sec-Fetch-Site: same-site, not
// cross-site. That was enough to approve a pairing request and hand a stranger a
// standing write relationship without the user clicking anything, which is
// precisely what the token was meant to prevent. loopbackOnly does not catch it
// either: the Host header is the dashboard's own, because the request really is
// addressed to the dashboard.
//
// The defence is a header the attacking page CANNOT SEND, not a port number:
//
//   - MutationHeader is required. A page on another origin cannot set a custom
//     header without a CORS preflight, and this server answers no preflight and
//     sends no CORS headers, so the browser never sends the real request. A form
//     POST cannot set headers at all. The dashboard's own page sets it freely,
//     because it IS same-origin and needs no preflight.
//   - Sec-Fetch-Site, when present, must be same-origin or none. The browser
//     labels the cross-port page same-site, which is exactly the case above.
//     Not required — non-browser clients never send it.
//   - Origin, when present, must be a loopback origin — ANY PORT. Not the
//     server's own port: an ssh -L tunnel to a headless machine is the
//     documented way to administer it, and its local port routinely differs
//     from the remote bound port. The two cases are byte-identical in Origin,
//     so port equality can never be the defence; it only breaks the tunnel.
//   - Origin absent is allowed: curl and the CLI send none, and carry a token.
//
// Reads are deliberately untouched: they change nothing, and /auth is a GET
// whose whole job is to be opened from outside the page (a typed or clicked URL
// arrives as Sec-Fetch-Site: none or cross-site, and with no Origin).
//
// This authorizes nothing on its own — requireToken still runs behind it.
func fromTheDashboardOnly(next http.Handler) http.Handler {
	const refused = "this request did not come from the dashboard page; changes can only be made in the dashboard itself"
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet, http.MethodHead, http.MethodOptions:
			next.ServeHTTP(w, r)
			return
		}
		if r.Header.Get(MutationHeader) == "" {
			http.Error(w, refused+" (no "+MutationHeader+" header)", http.StatusForbidden)
			return
		}
		switch r.Header.Get("Sec-Fetch-Site") {
		case "", "same-origin", "none":
		default:
			http.Error(w, refused, http.StatusForbidden)
			return
		}
		if origin := r.Header.Get("Origin"); origin != "" && !loopbackOrigin(origin) {
			http.Error(w, refused+" (origin "+origin+")", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// loopbackOrigin reports whether an Origin header names a page served by this
// machine. The port is deliberately not checked — see fromTheDashboardOnly.
func loopbackOrigin(origin string) bool {
	u, err := url.Parse(origin)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") {
		return false
	}
	return loopbackHost(u.Hostname())
}

func (s *Server) Serve() error {
	err := s.http.Serve(s.ln)
	if err == http.ErrServerClosed {
		return nil
	}
	return err
}

func (s *Server) Shutdown() {
	// Release the event streams first: they are deliberately long-lived, and
	// Shutdown would otherwise sit out its whole timeout waiting for them.
	s.doneOnce.Do(func() { close(s.done) })
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
	s.setSession(w)
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

// sessionFor builds the dashboard's session cookie.
//
// It outlives the browser session so http://127.0.0.1:<port> can simply be
// bookmarked; without that the cookie dies on browser close and people
// bookmark the ?token=... URL instead, putting a live credential somewhere
// browser sync can carry it off the machine.
//
// The value IS the IPC token, so this leaves a working credential in the
// browser profile. What makes that acceptable is what already guards it: the
// listener binds loopback only, loopbackOnly rejects any other Host, and
// fromTheDashboardOnly blocks cross-origin writes. Anyone who can read this
// profile can already read state.json.
func (s *Server) setSession(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookie,
		Value:    s.state.IPCToken,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
		// Outlives the browser session so http://127.0.0.1:<port> can simply be
		// bookmarked. Without this the cookie dies on browser close and the
		// bookmark bounces, which pushes people into bookmarking the
		// ?token=... URL instead — a live credential sitting in a syncable
		// bookmark, which is worse.
		//
		// The value IS the IPC token, so this leaves a working credential in
		// the browser profile. What makes that acceptable is what already
		// guards it within a session: the listener binds loopback only,
		// loopbackOnly rejects any other Host, and fromTheDashboardOnly blocks
		// cross-origin writes. Anyone who can read this profile can already
		// read state.json.
		MaxAge: int(sessionLifetime.Seconds()),
	})
}

// requireToken authorizes a request via Bearer token (CLI) or session cookie
// (browser). Binding to loopback keeps the LAN out; the token keeps other
// local users on multi-user machines out.
//
// The token alone is NOT enough for that last part in a browser: the cookie
// rides along on a request made by a page on any other loopback port, so
// fromTheDashboardOnly guards every state-changing request in front of this.
func (s *Server) requireToken(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if bearer, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer "); ok && s.tokenOK(bearer) {
			next(w, r)
			return
		}
		if c, err := r.Cookie(sessionCookie); err == nil && s.tokenOK(c.Value) {
			// Slide the expiry forward on every authenticated request, so a
			// dashboard someone actually uses never quietly stops working and
			// sends them back to the terminal. It still lapses on its own if
			// the machine goes untouched for the full window.
			s.setSession(w)
			next(w, r)
			return
		}
		http.Error(w, "unauthorized", http.StatusUnauthorized)
	}
}

// staticAssets serves the dashboard's own files with a validator attached.
//
// THE BUG THIS FIXES, and it is not a small one: embed.FS carries no
// modification times, so net/http sends the dashboard's HTML, CSS and
// JavaScript with no Last-Modified, no ETag and no Cache-Control whatsoever.
// With nothing to revalidate against, a browser is free to keep serving the
// copy it already has — so somebody upgrades backup-maker, restarts it, reloads
// the page, and is still looking at yesterday's dashboard. Every fix shipped to
// this UI would appear not to have landed, which is exactly how it was found.
//
// The tag is the build, not the release version: a developer rebuilding from a
// dirty tree gets a new one on every build, where version.Short() alone would
// read "dev-dirty" for ever and cache the same stale answer.
func staticAssets(fsys fs.FS) http.Handler {
	files := http.FileServerFS(fsys)
	info := version.Get()
	tag := `"` + info.Version + "-" + info.Commit + "-" + info.Date + `"`
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// no-cache means "you may keep it, but ask me before using it" — not
		// no-store. The 304 below is what keeps that cheap.
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("ETag", tag)
		if r.Header.Get("If-None-Match") == tag {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		files.ServeHTTP(w, r)
	})
}
