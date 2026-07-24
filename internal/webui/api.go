// SPDX-License-Identifier: MIT

package webui

import (
	"encoding/json"
	"github.com/phil9922/backup-maker/internal/browse"
	"net/http"
)

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func (s *Server) handlePing(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, map[string]string{"ping": "pong"})
}

func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, s.status())
}

// handleScan runs an on-demand LAN sweep for network drives. Never triggered
// by anything but an explicit user action.
func (s *Server) handleScan(w http.ResponseWriter, r *http.Request) {
	hosts, err := s.actions.Scan(r.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	writeJSON(w, hosts)
}

func (s *Server) handleAddShare(w http.ResponseWriter, r *http.Request) {
	var req AddShareRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&req); err != nil {
		http.Error(w, "bad request: "+err.Error(), http.StatusBadRequest)
		return
	}
	if err := s.actions.AddShare(req); err != nil {
		http.Error(w, err.Error(), http.StatusUnprocessableEntity)
		return
	}
	writeJSON(w, map[string]bool{"ok": true})
}

func (s *Server) handleWake(w http.ResponseWriter, r *http.Request) {
	var req WakeRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&req); err != nil {
		http.Error(w, "bad request: "+err.Error(), http.StatusBadRequest)
		return
	}
	if s.actions.Wake == nil {
		http.Error(w, "wake unavailable", http.StatusServiceUnavailable)
		return
	}
	if err := s.actions.Wake(req.Target); err != nil {
		http.Error(w, err.Error(), http.StatusUnprocessableEntity)
		return
	}
	// A sent packet is not a woken machine; say so rather than implying the
	// target is back.
	writeJSON(w, map[string]any{
		"ok":      true,
		"message": "wake packet sent; the target may take a minute to come back",
	})
}

// decodeJSON reads a bounded JSON body into v.
func decodeJSON(w http.ResponseWriter, r *http.Request, v any) bool {
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(v); err != nil {
		http.Error(w, "bad request: "+err.Error(), http.StatusBadRequest)
		return false
	}
	return true
}

// unavailable reports an action the daemon didn't wire up, rather than
// panicking on a nil func.
func unavailable(w http.ResponseWriter, name string) {
	http.Error(w, name+" unavailable", http.StatusServiceUnavailable)
}

func (s *Server) handleBrowse(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Query().Get("path")
	if path == "" {
		writeJSON(w, map[string]any{"roots": browse.Roots()})
		return
	}
	listing, err := browse.Dirs(path)
	if err != nil {
		http.Error(w, err.Error(), http.StatusUnprocessableEntity)
		return
	}
	writeJSON(w, listing)
}

func (s *Server) handleMachines(w http.ResponseWriter, r *http.Request) {
	if s.actions.Machines == nil {
		unavailable(w, "machine list")
		return
	}
	// Scanning the network is never implicit: the caller must ask for it.
	scan := r.URL.Query().Get("scan") == "1"
	out, err := s.actions.Machines(r.Context(), scan)
	if err != nil {
		http.Error(w, err.Error(), http.StatusUnprocessableEntity)
		return
	}
	writeJSON(w, out)
}

func (s *Server) handleStorage(w http.ResponseWriter, r *http.Request) {
	var req StorageRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	if s.actions.Storage == nil {
		unavailable(w, "storage list")
		return
	}
	out, err := s.actions.Storage(r.Context(), req)
	if err != nil {
		http.Error(w, err.Error(), http.StatusUnprocessableEntity)
		return
	}
	writeJSON(w, out)
}

func (s *Server) handleCreateBackup(w http.ResponseWriter, r *http.Request) {
	var req BackupRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	if s.actions.CreateBackup == nil {
		unavailable(w, "backup creation")
		return
	}
	out, err := s.actions.CreateBackup(req)
	if err != nil {
		// Nothing was saved: the whole request is validated before any write.
		http.Error(w, err.Error(), http.StatusUnprocessableEntity)
		return
	}
	writeJSON(w, out)
}

func (s *Server) handleRemoveFolder(w http.ResponseWriter, r *http.Request) {
	if s.actions.RemoveFolder == nil {
		unavailable(w, "folder removal")
		return
	}
	if err := s.actions.RemoveFolder(r.PathValue("id")); err != nil {
		http.Error(w, err.Error(), http.StatusUnprocessableEntity)
		return
	}
	writeJSON(w, map[string]any{
		"ok":      true,
		"message": "stopped backing up this folder; files already on your targets were left alone",
	})
}

func (s *Server) handleRemoveTarget(w http.ResponseWriter, r *http.Request) {
	if s.actions.RemoveTarget == nil {
		unavailable(w, "target removal")
		return
	}
	if err := s.actions.RemoveTarget(r.PathValue("name")); err != nil {
		http.Error(w, err.Error(), http.StatusUnprocessableEntity)
		return
	}
	writeJSON(w, map[string]any{
		"ok":      true,
		"message": "stopped backing up to this destination; the backups already there were left alone",
	})
}

func (s *Server) handleAddArchive(w http.ResponseWriter, r *http.Request) {
	var req ArchiveRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	if s.actions.AddArchive == nil {
		unavailable(w, "scheduled snapshots")
		return
	}
	if err := s.actions.AddArchive(req); err != nil {
		http.Error(w, err.Error(), http.StatusUnprocessableEntity)
		return
	}
	writeJSON(w, map[string]bool{"ok": true})
}

func (s *Server) handleCompleteSetup(w http.ResponseWriter, r *http.Request) {
	if s.actions.CompleteSetup == nil {
		unavailable(w, "setup")
		return
	}
	if err := s.actions.CompleteSetup(); err != nil {
		http.Error(w, err.Error(), http.StatusUnprocessableEntity)
		return
	}
	writeJSON(w, map[string]bool{"ok": true})
}
