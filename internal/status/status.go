// SPDX-License-Identifier: MIT

// Package status folds syncthing cluster knowledge and local-mirror engine
// state into one health model, shared by the CLI table and the dashboard.
package status

import (
	"strings"
	"time"

	"github.com/phil9922/backup-maker/internal/archive"
	"github.com/phil9922/backup-maker/internal/config"
	"github.com/phil9922/backup-maker/internal/localmirror"
	"github.com/phil9922/backup-maker/internal/pairing"
	"github.com/phil9922/backup-maker/internal/syncthing"
)

type Model struct {
	MachineName string `json:"machine_name"`
	DeviceID    string `json:"device_id"`
	// EngineNeeded is false when no machine targets or receiving are
	// configured — the sync engine then stays off by design (and was never
	// downloaded), which is not an error state.
	EngineNeeded bool `json:"engine_needed"`
	EngineOK     bool `json:"engine_ok"`
	// SetupComplete is false on a fresh install, which is what makes the
	// dashboard open the setup wizard instead of an empty table.
	SetupComplete  bool                    `json:"setup_complete"`
	Folders        []FolderInfo            `json:"folders"`
	Targets        []TargetInfo            `json:"targets"`
	Rows           []Row                   `json:"rows"`
	Archives       []ArchiveRow            `json:"archives,omitempty"`
	Receive        ReceiveInfo             `json:"receive"`
	PendingSources []pairing.PendingSource `json:"pending_sources,omitempty"`
}

// ArchiveRow is one scheduled-archive job's health line.
type ArchiveRow struct {
	Name    string    `json:"name"`
	Target  string    `json:"target"`
	Every   string    `json:"every"`
	LastRun time.Time `json:"last_run,omitzero"`
	NextDue time.Time `json:"next_due,omitzero"`
	State   string    `json:"state"` // ok | due | failed | never run
	Detail  string    `json:"detail,omitempty"`
}

// FolderInfo is one protected folder, for the dashboard's folder panel.
type FolderInfo struct {
	ID      string   `json:"id"`
	Label   string   `json:"label"`
	Path    string   `json:"path"`
	Ignores []string `json:"ignores,omitempty"`
}

// TargetInfo is one configured destination.
//
// Location is the field the dashboard was missing entirely: without it a
// target called "sdcard" is just a name with no way to tell what or where it
// is, which is exactly how the old UI confused people.
type TargetInfo struct {
	Name     string `json:"name"`
	Type     string `json:"type"`
	Location string `json:"location"`
	// FolderCount is how many folders back up here; 0 means every folder.
	FolderCount int       `json:"folder_count"`
	AllFolders  bool      `json:"all_folders"`
	State       string    `json:"state"`
	LastSeen    time.Time `json:"last_seen,omitzero"`
	WakeEnabled bool      `json:"wake_enabled,omitempty"`
	// ReclaimNote records the most recent automatic deletion of old backup
	// history to free space. Never left silent: the user must be able to see
	// that something was removed.
	ReclaimNote string `json:"reclaim_note,omitempty"`
	// FreeBytes/TotalBytes/SpaceReportedAt describe how full the destination
	// is. SpaceReportedAt is when the reading was last taken, so the UI can
	// grey a stale figure ("as of 2h ago") instead of pretending it is live —
	// a sleeping NAS keeps its last-known-good value rather than losing the bar.
	// All zero means the destination never reported (a paired machine, or one
	// that has never been reachable).
	FreeBytes       uint64    `json:"free_bytes,omitempty"`
	TotalBytes      uint64    `json:"total_bytes,omitempty"`
	SpaceReportedAt time.Time `json:"space_reported_at,omitzero"`
	// MinFreeBytes is the reclaim reserve kept free on this destination, so the
	// dashboard can say "keeping 20GB free". 0 means reclaiming is off.
	MinFreeBytes uint64 `json:"min_free_bytes,omitempty"`
}

// SpaceSample is one destination's last-known-good free/total, with the time it
// was taken. The daemon samples these off its already-open destination
// connections; the collector folds them into TargetInfo.
type SpaceSample struct {
	Free  uint64
	Total uint64
	At    time.Time
}

// Row is one folder × target health line.
type Row struct {
	FolderID    string    `json:"folder_id"`
	FolderLabel string    `json:"folder_label"`
	FolderPath  string    `json:"folder_path"`
	TargetName  string    `json:"target_name"`
	TargetType  string    `json:"target_type"`
	State       string    `json:"state"` // in sync | syncing | offline | stale | wrong-drive | error
	Completion  float64   `json:"completion"`
	NeedItems   int       `json:"need_items"`
	NeedBytes   int64     `json:"need_bytes"`
	LastSeen    time.Time `json:"last_seen,omitzero"`
	Stale       bool      `json:"stale"`
	// TransferredBytes/TotalBytes describe the transfer currently in flight,
	// so the UI can render "412MB of 2.9GB" alongside the bar. Both 0 means
	// nothing is pending.
	TransferredBytes int64 `json:"transferred_bytes,omitempty"`
	TotalBytes       int64 `json:"total_bytes,omitempty"`
	// WakeEnabled reports that this target has a MAC address configured, so
	// the daemon tries to wake it while it's offline. It does not mean the
	// target can actually be woken — that depends on its own BIOS/OS setup.
	WakeEnabled bool   `json:"wake_enabled,omitempty"`
	Detail      string `json:"detail,omitempty"`
}

type ReceiveInfo struct {
	Enabled bool   `json:"enabled"`
	Root    string `json:"root,omitempty"`
}

// Collector gathers the model on demand. Client returns nil while the (lazy)
// sync engine isn't running. Archives returns recent job results and
// last-run times.
type Collector struct {
	Cfg      func() *config.Config
	Client   func() *syncthing.Client
	Engines  func() []*localmirror.Engine
	Archives func() ([]archive.Result, map[string]time.Time)
	// SetupDone reports the persisted "don't show the wizard again" flag.
	SetupDone func() bool
	// Space returns per-destination free/total usage, keyed by target name.
	// nil (or a missing key) simply leaves a target's space fields empty.
	Space func() map[string]SpaceSample
}

func (col *Collector) Collect() Model {
	cfg := col.Cfg()
	m := Model{
		MachineName: cfg.General.MachineName,
		Receive:     ReceiveInfo{Enabled: cfg.Receive.Enabled, Root: cfg.Receive.Root},
	}
	m.EngineNeeded = cfg.Receive.Enabled
	for _, t := range cfg.Targets {
		if t.Type == "device" {
			m.EngineNeeded = true
		}
	}

	client := col.Client()
	if client != nil {
		if id, err := client.MyID(); err == nil {
			m.DeviceID = id
			m.EngineOK = true
		}
	}

	staleAfter := time.Duration(cfg.Defaults.StaleAfterDays) * 24 * time.Hour
	if staleAfter <= 0 {
		staleAfter = config.DefaultStaleAfterDays * 24 * time.Hour
	}

	// Device targets: ask syncthing about remote completion.
	var conns *syncthing.Connections
	var stats map[string]syncthing.DeviceStats
	if m.EngineOK {
		conns, _ = client.Connections()
		stats, _ = client.DeviceStats()
	}
	for _, t := range cfg.Targets {
		if t.Type != "device" {
			continue
		}
		connected := false
		if conns != nil {
			if c, ok := conns.Connections[t.DeviceID]; ok {
				connected = c.Connected
			}
		}
		var lastSeen time.Time
		if s, ok := stats[t.DeviceID]; ok {
			lastSeen, _ = time.Parse(time.RFC3339Nano, s.LastSeen)
		}
		for _, f := range cfg.FoldersForTarget(t) {
			row := Row{
				FolderID:    f.ID,
				FolderLabel: f.Label,
				FolderPath:  f.Path,
				TargetName:  t.Name,
				TargetType:  t.Type,
				LastSeen:    lastSeen,
			}
			if !m.EngineOK {
				row.State = "error"
				row.Detail = "sync engine not running"
			} else if comp, err := client.Completion(f.ID, t.DeviceID); err == nil {
				row.Completion = comp.Completion
				row.NeedItems = comp.NeedItems
				row.NeedBytes = comp.NeedBytes
				switch {
				case !connected && time.Since(lastSeen) > staleAfter:
					row.State = "stale"
					row.Stale = true
				case !connected:
					row.State = "offline"
				case comp.Completion >= 100:
					row.State = "in sync"
				default:
					row.State = "syncing"
				}
			} else {
				row.State = "error"
				row.Detail = err.Error()
			}
			m.Rows = append(m.Rows, row)
		}
	}

	// Drive targets: local engine snapshots.
	folderByID := map[string]config.Folder{}
	for _, f := range cfg.Folders {
		folderByID[f.ID] = f
	}
	for _, e := range col.Engines() {
		st := e.Status()
		f := folderByID[st.FolderID]
		row := Row{
			FolderID:    st.FolderID,
			FolderLabel: f.Label,
			FolderPath:  f.Path,
			TargetName:  st.TargetName,
			TargetType:  st.TargetType,
			State:       st.State,
			LastSeen:    st.LastSync,
		}
		// Real transfer progress, so a drive/share row animates like a device
		// row instead of jumping 0 → 100 when the pass ends.
		row.Completion = st.Completion()
		row.NeedItems = st.TotalFiles - st.DoneFiles
		if row.NeedItems < 0 {
			row.NeedItems = 0
		}
		row.NeedBytes = st.TotalBytes - st.DoneBytes
		if row.NeedBytes < 0 {
			row.NeedBytes = 0
		}
		row.TransferredBytes = st.DoneBytes
		row.TotalBytes = st.TotalBytes
		if !st.LastSync.IsZero() && time.Since(st.LastSync) > staleAfter && st.State != "in sync" {
			row.State = "stale"
			row.Stale = true
		}
		if n := len(st.FileErrors); n > 0 {
			row.Detail = firstError(st.FileErrors, n)
		}
		m.Rows = append(m.Rows, row)
	}

	// Wake-on-LAN opt-in, applied to both row sources in one pass.
	wakeable := map[string]bool{}
	for _, t := range cfg.Targets {
		if t.WakeEnabled() {
			wakeable[t.Name] = true
		}
	}
	for i := range m.Rows {
		m.Rows[i].WakeEnabled = wakeable[m.Rows[i].TargetName]
	}

	// Space reclaimed per destination, so the dashboard can say what was
	// deleted rather than history quietly vanishing.
	reclaimNotes := map[string]string{}
	for _, e := range col.Engines() {
		if when, text := e.ReclaimNote(); text != "" && !when.IsZero() {
			reclaimNotes[e.TargetName] = text
		}
	}

	// Folder and target panels: what the dashboard is actually configured to
	// do, as opposed to the folder × target health matrix.
	for _, f := range cfg.Folders {
		m.Folders = append(m.Folders, FolderInfo{
			ID: f.ID, Label: f.Label, Path: f.Path, Ignores: f.ExtraIgnore,
		})
	}
	var space map[string]SpaceSample
	if col.Space != nil {
		space = col.Space()
	}
	for _, t := range cfg.Targets {
		info := TargetInfo{
			Name:         t.Name,
			Type:         t.Type,
			Location:     TargetLocation(t),
			FolderCount:  len(t.Folders),
			AllFolders:   len(t.Folders) == 0,
			WakeEnabled:  t.WakeEnabled(),
			MinFreeBytes: cfg.MinFreeBytes(t),
		}
		info.State, info.LastSeen = rollUp(m.Rows, t.Name)
		info.ReclaimNote = reclaimNotes[t.Name]
		if s, ok := space[t.Name]; ok {
			info.FreeBytes = s.Free
			info.TotalBytes = s.Total
			info.SpaceReportedAt = s.At
		}
		m.Targets = append(m.Targets, info)
	}

	// The wizard is owed to anyone who hasn't finished setting up. Treating
	// "has a folder AND a target" as done means a CLI-configured machine never
	// gets nagged, regardless of the flag.
	configured := len(cfg.Folders) > 0 && len(cfg.Targets) > 0
	flagged := col.SetupDone != nil && col.SetupDone()
	m.SetupComplete = configured || flagged

	// Scheduled archive jobs.
	if col.Archives != nil && len(cfg.Archives) > 0 {
		results, lastRuns := col.Archives()
		resultByName := map[string]archive.Result{}
		for _, r := range results {
			resultByName[r.ArchiveName] = r
		}
		for _, job := range cfg.Archives {
			row := ArchiveRow{Name: job.Name, Target: job.Target, Every: job.Every}
			every, _ := config.ParseEvery(job.Every)
			row.LastRun = lastRuns[job.Name]
			if !row.LastRun.IsZero() && every > 0 {
				row.NextDue = row.LastRun.Add(every)
			}
			res, hasResult := resultByName[job.Name]
			switch {
			case hasResult && res.Err != "":
				row.State = "failed"
				row.Detail = res.Err
			case row.LastRun.IsZero():
				row.State = "never run"
			case every > 0 && time.Since(row.LastRun) > every+time.Hour:
				row.State = "due"
			default:
				row.State = "ok"
			}
			m.Archives = append(m.Archives, row)
		}
	}

	if m.EngineOK && cfg.Receive.Enabled {
		if pend, err := pairing.PendingSources(client, cfg); err == nil {
			m.PendingSources = pend
		}
	}
	return m
}

func firstError(errs map[string]string, n int) string {
	for path, msg := range errs {
		if n > 1 {
			return path + ": " + msg + " (+ more)"
		}
		return path + ": " + msg
	}
	return ""
}

// TargetLocation renders where a target actually is, in the words a user would
// recognise: a mount path, a network address, or a shortened device ID.
func TargetLocation(t config.Target) string {
	switch t.Type {
	case "drive":
		return t.Path
	case "share":
		return t.URL
	case "device":
		if i := strings.IndexByte(t.DeviceID, '-'); i > 0 {
			return t.DeviceID[:i] + "…"
		}
		return t.DeviceID
	}
	return ""
}

// rollUp reduces a target's per-folder rows to one headline state. The worst
// state wins: a target with one broken folder is not "in sync", and saying so
// would be the kind of false reassurance a backup tool must never give.
func rollUp(rows []Row, target string) (string, time.Time) {
	rank := map[string]int{
		"in sync": 0, "syncing": 1, "scanning": 1,
		"offline": 2, "stale": 3, "full": 4, "wrong-drive": 5, "error": 6,
	}
	state := ""
	var last time.Time
	for _, r := range rows {
		if r.TargetName != target {
			continue
		}
		if state == "" || rank[r.State] > rank[state] {
			state = r.State
		}
		if r.LastSeen.After(last) {
			last = r.LastSeen
		}
	}
	if state == "" {
		return "no folders assigned", last
	}
	return state, last
}
