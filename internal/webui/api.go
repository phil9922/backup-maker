// SPDX-License-Identifier: MIT

package webui

import (
	"encoding/json"
	"net/http"
	"strings"

	qrcode "github.com/skip2/go-qrcode"

	"github.com/phil9922/backup-maker/internal/browse"
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

// handleAcceptPair approves a machine that is waiting to back up here — the
// dashboard's equivalent of "backup-maker pair accept".
//
// Loopback only, like every other route that changes configuration: the LAN
// view's allow-list does not carry it, so approving another machine can only
// ever be done at this computer.
func (s *Server) handleAcceptPair(w http.ResponseWriter, r *http.Request) {
	var req PairAcceptRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	if strings.TrimSpace(req.DeviceID) == "" {
		http.Error(w, "bad request: no device id", http.StatusBadRequest)
		return
	}
	if s.actions.AcceptPair == nil {
		unavailable(w, "pairing")
		return
	}
	// An ID that isn't waiting to pair — malformed, stale, or simply never
	// asked — lands here, and is refused rather than being added to the
	// approved list on the strength of the request alone.
	if err := s.actions.AcceptPair(req.DeviceID); err != nil {
		http.Error(w, err.Error(), http.StatusUnprocessableEntity)
		return
	}
	writeJSON(w, map[string]any{
		"ok":      true,
		"message": "approved; its folders will start arriving shortly",
	})
}

// handleRevertFolder discards changes made on this machine to a backup it holds
// for another machine, restoring the sender's copy.
//
// Loopback only, like every other route that changes something: the LAN view's
// allow-list does not carry it. That matters more here than most — the operation
// DELETES files that exist only on this machine — so it can only ever be started
// at this computer.
//
// Which folders may be named is decided by the daemon, not here: a folder this
// machine sends is refused, and comes back as a 422.
func (s *Server) handleRevertFolder(w http.ResponseWriter, r *http.Request) {
	var req ReceiveRevertRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	if strings.TrimSpace(req.FolderID) == "" {
		http.Error(w, "bad request: no folder id", http.StatusBadRequest)
		return
	}
	if s.actions.RevertFolder == nil {
		unavailable(w, "revert")
		return
	}
	if err := s.actions.RevertFolder(req.FolderID); err != nil {
		http.Error(w, err.Error(), http.StatusUnprocessableEntity)
		return
	}
	writeJSON(w, map[string]any{
		"ok":      true,
		"message": "restoring this folder from the machine that sent it; it may take a moment",
	})
}

// qrPixels is the rendered size of the pairing QR. Big enough that a phone
// camera locks on from a comfortable distance; the page scales it down to fit.
const qrPixels = 256

// handlePairQR renders this machine's device ID as a QR code, so the other
// machine's user can point a phone at it instead of transcribing 63 characters
// by hand.
//
// The payload is the bare ID and nothing else: a phone camera shows it as
// selectable text, which is the whole point, and any smarter encoding would
// only be readable by software that doesn't exist yet.
func (s *Server) handlePairQR(w http.ResponseWriter, r *http.Request) {
	if s.actions.DeviceID == nil {
		unavailable(w, "device id")
		return
	}
	id, err := s.actions.DeviceID()
	if err != nil || id == "" {
		// Not an error the user caused: machine sync simply isn't running yet,
		// so there is no identity to draw.
		http.Error(w, "no device id yet — the machine-sync engine isn't running",
			http.StatusServiceUnavailable)
		return
	}
	png, err := qrcode.Encode(id, qrcode.Medium, qrPixels)
	if err != nil {
		http.Error(w, err.Error(), http.StatusUnprocessableEntity)
		return
	}
	w.Header().Set("Content-Type", "image/png")
	// The ID is stable for the life of the machine, but caching it would
	// outlive an adoption that gives this machine a new identity.
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(png)
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
		"ok": true,
		"message": "stopped backing up this folder; it has moved to \"No longer protected\", " +
			"and the copies already on your destinations were left exactly where they are",
	})
}

// handleReenableFolder puts a stopped folder back into service.
func (s *Server) handleReenableFolder(w http.ResponseWriter, r *http.Request) {
	if s.actions.ReenableFolder == nil {
		unavailable(w, "turning a stopped folder back on")
		return
	}
	out, err := s.actions.ReenableFolder(r.PathValue("id"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusUnprocessableEntity)
		return
	}
	writeJSON(w, out)
}

// handleDeleteRetiredBackups deletes a stopped folder's backed-up copies.
//
// The only route on this API that removes a user's backups. The confirmation
// is re-checked by the daemon: a page can be reloaded, scripted, or simply
// wrong, and the check that matters is the one nearest the deletion.
func (s *Server) handleDeleteRetiredBackups(w http.ResponseWriter, r *http.Request) {
	if s.actions.DeleteRetiredBackups == nil {
		unavailable(w, "deleting backups")
		return
	}
	var req RetiredDeleteRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	if strings.TrimSpace(req.Confirm) == "" {
		http.Error(w, "nothing was deleted: type the folder's name to confirm", http.StatusUnprocessableEntity)
		return
	}
	out, err := s.actions.DeleteRetiredBackups(r.PathValue("id"), req.Confirm)
	if err != nil {
		http.Error(w, err.Error(), http.StatusUnprocessableEntity)
		return
	}
	writeJSON(w, out)
}

// handleForgetRetired drops the record and touches no files.
func (s *Server) handleForgetRetired(w http.ResponseWriter, r *http.Request) {
	if s.actions.ForgetRetired == nil {
		unavailable(w, "forgetting a stopped folder")
		return
	}
	if err := s.actions.ForgetRetired(r.PathValue("id")); err != nil {
		http.Error(w, err.Error(), http.StatusUnprocessableEntity)
		return
	}
	writeJSON(w, map[string]any{
		"ok":      true,
		"message": "forgotten; nothing on your destinations was touched",
	})
}

// handleSetFolderIgnores replaces one folder's exclude patterns. Only the
// config is written; the daemon's config watcher rebuilds the engines, exactly
// as it does for the CLI paths.
func (s *Server) handleSetFolderIgnores(w http.ResponseWriter, r *http.Request) {
	var req FolderIgnoresRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	if s.actions.SetFolderIgnores == nil {
		unavailable(w, "folder excludes")
		return
	}
	if err := s.actions.SetFolderIgnores(r.PathValue("id"), req.Ignores, req.NoDefaultIgnores); err != nil {
		http.Error(w, err.Error(), http.StatusUnprocessableEntity)
		return
	}
	writeJSON(w, map[string]bool{"ok": true})
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

// adoptGate enforces that adoption is only reachable while nothing is
// configured. 409 tells the frontend "this machine moved on" — distinct from
// a bad request or a failed connection.
func (s *Server) adoptGate(w http.ResponseWriter) bool {
	if s.actions.AdoptAllowed == nil {
		unavailable(w, "adoption")
		return false
	}
	if err := s.actions.AdoptAllowed(); err != nil {
		http.Error(w, err.Error(), http.StatusConflict)
		return false
	}
	return true
}

func (s *Server) handleAdoptScan(w http.ResponseWriter, r *http.Request) {
	if !s.adoptGate(w) {
		return
	}
	if s.actions.AdoptScan == nil {
		unavailable(w, "adoption scan")
		return
	}
	out, err := s.actions.AdoptScan()
	if err != nil {
		http.Error(w, err.Error(), http.StatusUnprocessableEntity)
		return
	}
	writeJSON(w, out)
}

func (s *Server) handleAdoptInspect(w http.ResponseWriter, r *http.Request) {
	if !s.adoptGate(w) {
		return
	}
	var req AdoptSourceRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	if s.actions.AdoptInspect == nil {
		unavailable(w, "adoption")
		return
	}
	out, err := s.actions.AdoptInspect(req)
	if err != nil {
		http.Error(w, err.Error(), http.StatusUnprocessableEntity)
		return
	}
	writeJSON(w, out)
}

func (s *Server) handleAdoptTestShare(w http.ResponseWriter, r *http.Request) {
	if !s.adoptGate(w) {
		return
	}
	var req TestShareRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	if s.actions.AdoptTestShare == nil {
		unavailable(w, "share testing")
		return
	}
	if err := s.actions.AdoptTestShare(req); err != nil {
		http.Error(w, err.Error(), http.StatusUnprocessableEntity)
		return
	}
	writeJSON(w, map[string]bool{"ok": true})
}

func (s *Server) handleAdopt(w http.ResponseWriter, r *http.Request) {
	if !s.adoptGate(w) {
		return
	}
	var req AdoptRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	if s.actions.Adopt == nil {
		unavailable(w, "adoption")
		return
	}
	out, err := s.actions.Adopt(req)
	if err != nil {
		// Nothing was saved: adoption validates before it writes.
		http.Error(w, err.Error(), http.StatusUnprocessableEntity)
		return
	}
	writeJSON(w, out)
}

// handleArchivePassword stores the password for an existing snapshot job.
// Deliberately NOT behind adoptGate: it is the post-adoption (or post-loss)
// recovery path on a configured machine.
func (s *Server) handleArchivePassword(w http.ResponseWriter, r *http.Request) {
	var req ArchivePasswordRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	if s.actions.SetArchivePassword == nil {
		unavailable(w, "snapshot passwords")
		return
	}
	if err := s.actions.SetArchivePassword(r.PathValue("name"), req.Password); err != nil {
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

// handleSetSettings changes [general]: which alerts the user wants, and whether
// the read-only network view runs.
//
// Loopback-only and CSRF-guarded like every other mutating route — and, like
// them, unreachable from the network view, which serves a fixed read-only
// allow-list and answers no POST at all. That matters more here than elsewhere:
// one of the settings on this route is the network view's own existence, and a
// view that could switch itself on, or switch the owner's alerts off, would be
// a hole in the one boundary this program's design rests on.
func (s *Server) handleSetSettings(w http.ResponseWriter, r *http.Request) {
	var req SettingsRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	if s.actions.SetSettings == nil {
		unavailable(w, "settings")
		return
	}
	if err := s.actions.SetSettings(req); err != nil {
		http.Error(w, err.Error(), http.StatusUnprocessableEntity)
		return
	}
	writeJSON(w, map[string]bool{"ok": true})
}

// TestAlertRequest names the delivery method to try.
type TestAlertRequest struct {
	Method string `json:"method"`
}

// handleTestAlert sends one notification by the named method and reports
// whether it arrived.
//
// The failure is the useful case. Desktop alerting depends on things
// backup-maker does not control — a notification daemon, a session bus, a
// machine with a screen at all — and the normal state of a working setup is
// silence, so "are alerts reaching me?" is otherwise unanswerable until the day
// something breaks. Loopback-only and CSRF-guarded like every other POST: this
// one causes a visible effect on the owner's desktop, which is not something a
// page on another origin gets to do.
func (s *Server) handleTestAlert(w http.ResponseWriter, r *http.Request) {
	var req TestAlertRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	if s.actions.TestAlert == nil {
		unavailable(w, "alert testing")
		return
	}
	if err := s.actions.TestAlert(r.Context(), req.Method); err != nil {
		http.Error(w, err.Error(), http.StatusUnprocessableEntity)
		return
	}
	writeJSON(w, map[string]bool{"ok": true})
}

// handleApproveLANDevice lets a device that is waiting read the network view.
//
// Loopback-only, like every other write — which is the point of the whole
// feature: the decision about who may watch this machine is made AT this
// machine, never from the network being admitted.
func (s *Server) handleApproveLANDevice(w http.ResponseWriter, r *http.Request) {
	if s.actions.ApproveLANDevice == nil {
		unavailable(w, "device approval")
		return
	}
	if err := s.actions.ApproveLANDevice(r.PathValue("code")); err != nil {
		http.Error(w, err.Error(), http.StatusUnprocessableEntity)
		return
	}
	writeJSON(w, map[string]bool{"ok": true})
}

// handleForgetLANDevice removes a device, approved or not. Removing an approved
// one revokes it: the browser's token stops matching anything and it is back to
// the holding page.
func (s *Server) handleForgetLANDevice(w http.ResponseWriter, r *http.Request) {
	if s.actions.ForgetLANDevice == nil {
		unavailable(w, "device approval")
		return
	}
	if err := s.actions.ForgetLANDevice(r.PathValue("code")); err != nil {
		http.Error(w, err.Error(), http.StatusUnprocessableEntity)
		return
	}
	writeJSON(w, map[string]bool{"ok": true})
}

// handleSetArchiveSchedule changes how often a snapshot runs and how many are
// kept. Only the config is written; the daemon's watcher picks it up.
func (s *Server) handleSetArchiveSchedule(w http.ResponseWriter, r *http.Request) {
	if s.actions.SetArchiveSchedule == nil {
		unavailable(w, "changing a snapshot schedule")
		return
	}
	var req ArchiveScheduleRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	if err := s.actions.SetArchiveSchedule(r.PathValue("name"), req.Every, req.Keep); err != nil {
		http.Error(w, err.Error(), http.StatusUnprocessableEntity)
		return
	}
	writeJSON(w, map[string]any{"ok": true, "message": "schedule updated"})
}

// handleSetArchivePaused stops or resumes a schedule.
func (s *Server) handleSetArchivePaused(w http.ResponseWriter, r *http.Request) {
	if s.actions.SetArchivePaused == nil {
		unavailable(w, "pausing a snapshot schedule")
		return
	}
	var req ArchivePausedRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	if err := s.actions.SetArchivePaused(r.PathValue("name"), req.Paused); err != nil {
		http.Error(w, err.Error(), http.StatusUnprocessableEntity)
		return
	}
	msg := "resumed — it will run at its next due time"
	if req.Paused {
		msg = "paused; nothing about it is lost, and the snapshots already written stay where they are"
	}
	writeJSON(w, map[string]any{"ok": true, "message": msg})
}

// handleRemoveArchive stops a schedule for good. The zips it wrote survive.
func (s *Server) handleRemoveArchive(w http.ResponseWriter, r *http.Request) {
	if s.actions.RemoveArchive == nil {
		unavailable(w, "removing a snapshot schedule")
		return
	}
	if err := s.actions.RemoveArchive(r.PathValue("name")); err != nil {
		http.Error(w, err.Error(), http.StatusUnprocessableEntity)
		return
	}
	writeJSON(w, map[string]any{
		"ok": true,
		"message": "this schedule will not run again; the snapshots it already wrote are left " +
			"exactly where they are, and still open with the password that made them",
	})
}
