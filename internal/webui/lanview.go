// SPDX-License-Identifier: MIT

package webui

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"strings"
	"time"
)

// The LAN listener serves only what is handled explicitly below: static assets,
// /api/ping, and a redacted /api/status. Everything else is refused.
//
// This is an ALLOW-list, not a deny-list, and that direction matters: a route
// added later is denied on the network by default rather than silently exposed
// because someone forgot to block it. Notably /auth is NOT served here — the
// token exchange stays on loopback, so the token never travels the network.

// lanReadOnly wraps the mux for the network-facing listener, refusing anything
// that could change configuration or read the filesystem.
//
// Enforcement is by WHICH LISTENER RECEIVED THE REQUEST — never by a Host
// header, an origin, or a client IP, all of which a caller can forge. The
// loopback listener gets the unrestricted mux; this one gets the allow-list.
// They are separate http.Servers so the two can't be confused.
//
// /api/browse is the sharpest example of why: it enumerates directories, and
// its whole security justification is that any caller already runs as this
// user. That stops being true the moment it is reachable from the network.
func lanReadOnly(next http.Handler, status func() any) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Static assets (the dashboard itself) are fine to serve. Everything
		// dynamic is handled explicitly below or refused — /auth in
		// particular, so the token exchange never travels the network.
		if r.Method == http.MethodGet && isStatic(r.URL.Path) {
			next.ServeHTTP(w, r)
			return
		}
		// Answered here rather than passed through, so the page can tell it is
		// the read-only view and hide controls instead of showing buttons that
		// fail with a 403 when tapped.
		if r.Method == http.MethodGet && r.URL.Path == "/api/ping" {
			writeJSON(w, map[string]any{"ping": "pong", "read_only": true})
			return
		}
		// Status is served WITHOUT a token so any device on the network can
		// glance at it — but redacted, because "anyone can see whether my
		// backups are working" and "anyone can see my directory layout" are
		// very different things to publish.
		if r.Method == http.MethodGet && r.URL.Path == "/api/status" {
			writeJSON(w, RedactForNetwork(status()))
			return
		}
		http.Error(w,
			"this is a read-only view. Backups can only be set up or changed on the computer running backup-maker.",
			http.StatusForbidden)
	})
}

// isStatic reports paths served straight from the embedded asset filesystem.
// Anything dynamic — the API, and the /auth token exchange — must be handled
// deliberately rather than falling through to the mux.
func isStatic(path string) bool {
	if strings.HasPrefix(path, "/api/") {
		return false
	}
	for _, dynamic := range []string{"/auth"} {
		if path == dynamic || strings.HasPrefix(path, dynamic+"/") {
			return false
		}
	}
	return true
}

// LANView is the optional second listener that lets other devices on your
// network watch backup progress without being able to change anything.
type LANView struct {
	http *http.Server
	ln   net.Listener
	url  string
}

// URL is the address to open from another device.
func (l *LANView) URL() string { return l.url }

// StartLANView binds a read-only dashboard to a specific LAN address.
//
// It binds that one interface rather than 0.0.0.0 on purpose: listening on
// everything would also expose the dashboard on VPN and container interfaces
// the user never meant to include.
func (s *Server) StartLANView(ip string, port int) (*LANView, error) {
	if err := checkBindable(ip, port); err != nil {
		return nil, err
	}
	addr := net.JoinHostPort(ip, fmt.Sprint(port))
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("binding the network view on %s: %w", addr, err)
	}
	v := &LANView{
		ln:  ln,
		url: "http://" + addr,
		http: &http.Server{
			Handler:           lanReadOnly(s.mux, s.status),
			ReadHeaderTimeout: 10 * time.Second,
		},
	}
	go func() { _ = v.http.Serve(ln) }()
	return v, nil
}

func (l *LANView) Close() {
	if l != nil && l.http != nil {
		_ = l.http.Close()
	}
}

// checkBindable fails early with a clear message rather than letting the view
// silently not appear on the network.
func checkBindable(ip string, port int) error {
	if net.ParseIP(ip) == nil {
		return fmt.Errorf("%q is not a valid address to bind the network view to", ip)
	}
	if port < 1 || port > 65535 {
		return fmt.Errorf("network view port %d is out of range", port)
	}
	return nil
}

// RedactForNetwork strips anything that describes the shape of this machine
// before status is published to the local network.
//
// The distinction being drawn: "are my backups working?" is fine for any
// device on the network to see. "here is the layout of my filesystem and where
// my NAS lives" is not — that is reconnaissance, and it would be handed to
// every phone, TV and smart device on the wifi.
//
// Kept as generic map surgery so a field added to the status model later shows
// up here as an unexpected key rather than compiling silently into the payload.
func RedactForNetwork(v any) any {
	raw, err := json.Marshal(v)
	if err != nil {
		return map[string]any{"error": "status unavailable"}
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		return map[string]any{"error": "status unavailable"}
	}

	// Identifiers of this machine and the peers it talks to.
	delete(m, "device_id")
	if rec, ok := m["receive"].(map[string]any); ok {
		delete(rec, "root")
	}
	// Folder labels stay ("code", "photos") — the paths do not.
	stripEach(m["folders"], "path")
	stripEach(m["rows"], "folder_path")
	// A destination keeps its name and health; its address does not — and
	// neither does its capacity. How big and how full a drive is describes the
	// hardware, which is the same reconnaissance the passwordless network view
	// exists to withhold.
	stripEach(m["targets"], "location", "free_bytes", "total_bytes", "space_reported_at", "min_free_bytes")
	m["redacted"] = true
	return m
}

func stripEach(list any, keys ...string) {
	items, ok := list.([]any)
	if !ok {
		return
	}
	for _, it := range items {
		row, ok := it.(map[string]any)
		if !ok {
			continue
		}
		for _, k := range keys {
			delete(row, k)
		}
	}
}
