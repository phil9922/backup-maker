// SPDX-License-Identifier: MIT

package webui

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// How often a connected dashboard's snapshot is re-evaluated, and how long an
// otherwise silent stream waits before writing a keep-alive. Unexported
// variables rather than constants so the tests can run the loop fast.
var (
	eventInterval  = time.Second
	eventHeartbeat = 25 * time.Second
)

// handleEvents streams the same snapshot /api/status returns, as server-sent
// events, so an open dashboard doesn't have to ask for it over and over.
//
// The saving is in what is NOT sent: the snapshot is evaluated every second but
// only written when it differs from the last one this client received, so a
// dashboard watching an idle machine costs one keep-alive comment every 25s and
// nothing else. /api/status stays the fallback for clients without EventSource.
func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request) {
	// Without flushing, events would sit in the response buffer and the stream
	// would look to the browser like a request that never answers. Refuse
	// rather than hang.
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}

	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-cache")
	h.Set("Connection", "keep-alive")
	// For anyone who puts a reverse proxy in front of the dashboard: nginx
	// buffers responses by default, which would batch the events up.
	h.Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	var last []byte
	// send reports whether the stream is still healthy; a write error means the
	// browser went away mid-event.
	send := func() bool {
		snapshot, err := json.Marshal(s.status())
		if err != nil {
			return false // a snapshot that can't be marshalled won't start working
		}
		if bytes.Equal(snapshot, last) {
			return true
		}
		last = snapshot
		if _, err := fmt.Fprintf(w, "data: %s\n\n", snapshot); err != nil {
			return false
		}
		flusher.Flush()
		return true
	}

	// Paint a freshly opened dashboard at once instead of making it wait out
	// the first tick.
	if !send() {
		return
	}

	ticker := time.NewTicker(eventInterval)
	defer ticker.Stop()
	// Its own ticker, not one reset by traffic: a redundant ping next to a real
	// event is harmless, and an idle stream is the case that matters.
	heartbeat := time.NewTicker(eventHeartbeat)
	defer heartbeat.Stop()

	for {
		select {
		case <-r.Context().Done():
			// The browser tab closed. Everything this handler owns is on this
			// goroutine, so returning leaves nothing behind.
			return
		case <-s.done:
			// The daemon is shutting down. Ending here lets Shutdown finish
			// immediately instead of waiting out its timeout on a connection
			// that would never go idle by itself.
			return
		case <-ticker.C:
			if !send() {
				return
			}
		case <-heartbeat.C:
			if _, err := io.WriteString(w, ": ping\n\n"); err != nil {
				return
			}
			flusher.Flush()
		}
	}
}
