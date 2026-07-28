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
	"github.com/phil9922/backup-maker/internal/version"
)

type Model struct {
	MachineName string `json:"machine_name"`
	// Version is the running build, so the dashboard can say which one it is
	// without anyone opening a terminal. Stripped from the network view: an
	// exact version tells whoever can read it which known bugs apply, and that
	// view is unauthenticated.
	Version string `json:"version"`
	// Commit is the build this binary was made from. Shown beside the version
	// because "dev-dirty" is the same string for every local build, so on a
	// machine that is not running a release it is the only thing that says
	// WHICH build is answering — which cost real confusion during development.
	// Stripped from the network view for exactly the same reason Version is.
	Commit   string `json:"commit,omitempty"`
	DeviceID string `json:"device_id"`
	// EngineNeeded is false when no machine targets or receiving are
	// configured — the sync engine then stays off by design (and was never
	// downloaded), which is not an error state.
	EngineNeeded bool `json:"engine_needed"`
	EngineOK     bool `json:"engine_ok"`
	// SetupComplete is false on a fresh install, which is what makes the
	// dashboard open the setup wizard instead of an empty table.
	SetupComplete   bool                    `json:"setup_complete"`
	Folders         []FolderInfo            `json:"folders"`
	Targets         []TargetInfo            `json:"targets"`
	Rows            []Row                   `json:"rows"`
	Archives        []ArchiveRow            `json:"archives,omitempty"`
	Receive         ReceiveInfo             `json:"receive"`
	ReceivedFolders []ReceivedFolderInfo    `json:"received_folders,omitempty"`
	PendingSources  []pairing.PendingSource `json:"pending_sources,omitempty"`
	Totals          Totals                  `json:"totals"`
	// Settings is what the dashboard's Settings panel renders and edits.
	// STRIPPED FROM THE NETWORK VIEW: which alerts this household has
	// switched off is a description of how closely they are watching, and
	// whether the LAN view is on is a fact about the listener a reader of that
	// view is already talking to. Neither is a health fact anyone in another
	// room can act on, which is the only test this view's contents pass.
	Settings Settings `json:"settings"`
}

// Settings mirrors the parts of [general] the browser is allowed to change.
// Deliberately not the whole section: machine name and ports are identity and
// plumbing, changed at setup, and a mis-click on either breaks a working setup
// in ways the dashboard cannot walk back.
type Settings struct {
	DesktopAlerts bool `json:"desktop_alerts"`
	// AlertKinds is flattened to plain booleans on purpose. The config keeps
	// tri-state pointers so an upgrade cannot silence anything by omission,
	// but a checkbox has two states, and the browser should be told the
	// resolved answer rather than asked to re-derive the default.
	BackupsStopped      bool `json:"alert_backups_stopped"`
	SnapshotFailed      bool `json:"alert_snapshot_failed"`
	UnrecognisedStorage bool `json:"alert_unrecognised_storage"`
	PairRequests        bool `json:"alert_pair_requests"`
	// WebhookEnabled and WebhookMinimal describe the webhook delivery method.
	WebhookEnabled bool `json:"webhook_enabled"`
	WebhookMinimal bool `json:"webhook_minimal"`
	// WebhookURLSet says whether an address is stored WITHOUT revealing it.
	// The URL is a credential — a Slack endpoint is a right to post — so the
	// panel is told that one exists, never what it is. Replacing it means
	// typing a new one, which is the right trade for not shipping a secret to
	// every browser that loads the dashboard.
	WebhookURLSet bool `json:"webhook_url_set"`
	// NtfyEnabled and NtfyMinimal describe the ntfy delivery method.
	NtfyEnabled bool `json:"ntfy_enabled"`
	NtfyMinimal bool `json:"ntfy_minimal"`
	// NtfyTopicSet and NtfyTokenSet say whether each is stored WITHOUT
	// revealing it. The topic is a credential in its own right on the public
	// server — the name is the only thing standing between a stranger and every
	// alert this machine sends — so it gets the webhook URL's treatment, not a
	// weaker one.
	NtfyTopicSet bool `json:"ntfy_topic_set"`
	NtfyTokenSet bool `json:"ntfy_token_set"`
	// NtfyTopicDisplay and WebhookURLDisplay are the saved addresses with the
	// secret half replaced by bullets — scheme and host only.
	//
	// THE HOST IS SHOWN ON PURPOSE. The panel used to say only THAT an address
	// was saved, never which, and a user who typed "nfty.sh" for "ntfy.sh" had
	// no way to see it: the address saved, delivered to the typo host, and a
	// real alert left the house. The host is what a person checks a typo
	// against and is not the secret; the path is (an ntfy topic is its own
	// access control, a Slack path is a right to post) and never leaves here.
	NtfyTopicDisplay  string `json:"ntfy_topic_display,omitempty"`
	WebhookURLDisplay string `json:"webhook_url_display,omitempty"`
	// Delivery is how each method last performed, so a delivery route that has
	// stopped working is visible instead of silent.
	Delivery []DeliveryInfo `json:"delivery,omitempty"`
	// LANView is the read-only view for phones and other machines. Reported
	// so the panel can show its state; changing it restarts that listener.
	LANView bool `json:"lan_view"`
	// LANViewURL is where that view is reachable, blank when it is off. The
	// dashboard shows it so the address can be typed into a phone.
	LANViewURL string `json:"lan_view_url,omitempty"`
	// LANViewAccess is "all" or "approved": who on the network may read the
	// view. Blank reads as "all".
	LANViewAccess string `json:"lan_view_access,omitempty"`
	// LANDevices is every device that has asked to read the network view,
	// waiting or approved. Only ever sent to the dashboard — the network view
	// itself must not be handed a roster of the household's phones, which is
	// why the whole Settings block is stripped from it.
	LANDevices []LANDeviceInfo `json:"lan_devices,omitempty"`
	// LANViewError is why it is switched on and yet not listening — the port
	// already taken by another backup-maker, no LAN address on this machine.
	// Reported rather than guessed at: the panel used to say "no network
	// address was found" for every failure, which was simply untrue for the
	// commonest one.
	LANViewError string `json:"lan_view_error,omitempty"`
}

// DeliveryInfo is one delivery method's last outcome. Shown under that method
// in the settings panel: an alerting route that has quietly stopped working is
// the one fault this program cannot report by alerting about it.
type DeliveryInfo struct {
	Method string    `json:"method"`
	At     time.Time `json:"at,omitzero"`
	OK     bool      `json:"ok"`
	// Error is the daemon's own message, never a guess. Empty when OK.
	Error string `json:"error,omitempty"`
}

// LANDeviceInfo is one browser that has asked to watch backups from the network.
//
// Identified to the user by a short CODE shown on both screens, because the
// question being answered is "is this the phone in my hand?" — and an address
// or a user-agent string cannot answer it. Both are carried anyway, as
// recognition aids only; neither decides anything.
type LANDeviceInfo struct {
	Code      string    `json:"code"`
	Approved  bool      `json:"approved"`
	Addr      string    `json:"addr,omitempty"`
	Kind      string    `json:"kind,omitempty"`
	FirstSeen time.Time `json:"first_seen,omitzero"`
	LastSeen  time.Time `json:"last_seen,omitzero"`
}

// Totals is the lifetime odometer: how much work backup-maker has done on this
// machine since it was installed. Not a gauge of what is protected now — a file
// copied ten times as it changed counts ten times, which is the point.
//
// WHAT IT DOES NOT COUNT, and why the target mix travels with it: data bound for
// a paired machine is transferred by the sync engine, not by our copy loop, so
// none of it reaches Bytes. Publishing the number alone would therefore be a
// quiet lie on a machine that backs up to another computer — and an outright
// one on a machine whose ONLY destinations are paired computers, where a
// confident "0 B backed up" would read as "nothing has ever been protected".
// MirrorTargets and DeviceTargets let every renderer say what the figure covers
// instead of guessing.
type Totals struct {
	Bytes uint64    `json:"bytes"`
	Files uint64    `json:"files"`
	Since time.Time `json:"since,omitzero"`
	// MirrorTargets is how many destinations backup-maker writes itself (drive
	// and share); DeviceTargets how many are paired computers.
	MirrorTargets int `json:"mirror_targets"`
	DeviceTargets int `json:"device_targets"`
}

// Counted reports whether the odometer covers this machine's destinations at
// all. False means every destination is a paired computer (or the counter has
// never been fed), so the figure is not a measurement of nothing — it is the
// absence of one, and must be said in those words rather than shown as zero.
func (t Totals) Counted() bool { return t.MirrorTargets > 0 || t.Bytes > 0 }

// Partial reports a figure that leaves real backups out: paired machines are
// configured, and what goes to them is transferred by the sync engine and never
// passes through the byte counter.
func (t Totals) Partial() bool { return t.DeviceTargets > 0 }

// ArchiveRow is one scheduled-archive job's health line.
type ArchiveRow struct {
	Name    string    `json:"name"`
	Target  string    `json:"target"`
	Every   string    `json:"every"`
	LastRun time.Time `json:"last_run,omitzero"`
	NextDue time.Time `json:"next_due,omitzero"`
	State   string    `json:"state"` // ok | due | failed | never run | needs password
	Detail  string    `json:"detail,omitempty"`
	// NeedsPassword marks a job with no stored zip password — it cannot run
	// until one is entered (an adoption may leave jobs in this state).
	NeedsPassword bool `json:"needs_password,omitempty"`
}

// FolderInfo is one protected folder, for the dashboard's folder panel.
type FolderInfo struct {
	ID      string   `json:"id"`
	Label   string   `json:"label"`
	Path    string   `json:"path"`
	Ignores []string `json:"ignores,omitempty"`
	// NoDefaultIgnores says this folder backs up the junk everything else
	// skips. Published so the dashboard's exclude editor can send the flag back
	// unchanged instead of silently switching the standard exclusions on again.
	NoDefaultIgnores bool `json:"no_default_ignores,omitempty"`
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
	// SpaceUnknown marks a destination that HAS a capacity but has never
	// managed to report it — some NAS firmware never answers the query at all.
	// Set only for drive/share destinations, never for a paired machine, which
	// owns its own storage and is legitimately blank here. The difference is
	// worth a field of its own because the two look identical as bare zeroes,
	// and one of them is a destination where the reclaim reserve is not being
	// enforced.
	SpaceUnknown bool `json:"space_unknown,omitempty"`
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
	// Unknown says the destination could not be measured at all and never has
	// been, as opposed to being offline with a reading still worth showing.
	Unknown bool
}

// Row is one folder × target health line.
type Row struct {
	FolderID    string    `json:"folder_id"`
	FolderLabel string    `json:"folder_label"`
	FolderPath  string    `json:"folder_path"`
	TargetName  string    `json:"target_name"`
	TargetType  string    `json:"target_type"`
	State       string    `json:"state"` // in sync | syncing | offline | stale | awaiting-pair | wrong-drive | error
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

// ReceivedFolderInfo is one backup this machine is HOLDING for another machine.
// These folders are not in config.toml (they are created in the engine as the
// offers arrive), so they produce no Rows and appear nowhere else in the model.
//
// It carries NO PATH, deliberately. Where received backups land is exactly what
// RedactForNetwork strips from receive.root, and a per-folder path would put it
// straight back on the network as <root>/<machine>/<label>. The label and the
// drift count are health facts of the same kind the network view already
// publishes; the location on disk is not, so it is never collected here at all
// rather than collected and stripped later.
type ReceivedFolderInfo struct {
	ID    string `json:"id"`
	Label string `json:"label"`
	// Source names the machine this backup came from — a name, like a target's,
	// never an address or a device ID.
	Source string `json:"source,omitempty"`
	// ChangedItems is how many files here differ from what the sending machine
	// holds because they were edited, deleted or created ON THIS MACHINE. It is
	// syncthing's receiveOnlyTotalItems: the count that includes local
	// deletions, which receiveOnlyChangedFiles leaves out.
	//
	// Anything above zero means this copy is no longer a faithful backup, and is
	// what the dashboard offers to revert.
	ChangedItems int   `json:"changed_items"`
	ChangedBytes int64 `json:"changed_bytes,omitempty"`
}

// Collector gathers the model on demand. Client returns nil while the (lazy)
// sync engine isn't running. Archives returns recent job results and
// last-run times.
type Collector struct {
	Cfg      func() *config.Config
	Client   func() *syncthing.Client
	Engines  func() []*localmirror.Engine
	Archives func() ([]archive.Result, map[string]time.Time)
	// HasArchivePassword reports whether a job's zip password is stored. nil
	// (not wired) means "assume yes" — only the daemon knows the state.
	HasArchivePassword func(name string) bool
	// SetupDone reports the persisted "don't show the wizard again" flag.
	SetupDone func() bool
	// Space returns per-destination free/total usage, keyed by target name.
	// nil (or a missing key) simply leaves a target's space fields empty.
	Space func() map[string]SpaceSample
	// Totals returns the lifetime bytes/files copied and when counting began.
	// nil leaves the odometer at zero, which the renderers read as "not counted
	// on this machine" rather than "nothing has been backed up".
	Totals func() (bytes, files uint64, since time.Time)
	// LANViewURL returns the address the read-only view is listening on, or ""
	// when it is off or was asked for and could not bind. nil leaves it blank,
	// which the panel renders as "not running".
	LANViewURL func() string
	// LANViewProblem returns why that view is not listening despite being
	// switched on, or "" when nothing is wrong.
	LANViewProblem func() string
	// LANDevices lists the devices that have asked to read the network view.
	LANDevices func() []LANDeviceInfo
	// WebhookURLSet reports whether an address is stored, never what it is.
	WebhookURLSet func() bool
	// NtfyTopicSet and NtfyTokenSet do the same for ntfy: whether each is
	// stored, never its value.
	NtfyTopicSet func() bool
	NtfyTokenSet func() bool
	// NtfyTopicDisplay and WebhookURLDisplay return those addresses redacted to
	// scheme+host, so the panel can prove which one is saved.
	NtfyTopicDisplay  func() string
	WebhookURLDisplay func() string
	// Delivery reports how each delivery method last performed.
	Delivery func() []DeliveryInfo
}

// shortCommit is the first seven characters of the build's git SHA, or "" when
// the binary carries none.
func shortCommit() string {
	c := version.Get().Commit
	if len(c) > 7 {
		return c[:7]
	}
	return c
}

// SettingsFrom resolves the config's tri-state alert switches into the plain
// booleans a checkbox needs.
func SettingsFrom(cfg *config.Config) Settings {
	a := cfg.General.Alerts
	access := cfg.General.LANViewAccess
	if access == "" {
		access = "all"
	}
	return Settings{
		DesktopAlerts:       cfg.General.DesktopAlerts,
		BackupsStopped:      a.BackupsStoppedOn(),
		SnapshotFailed:      a.SnapshotFailedOn(),
		UnrecognisedStorage: a.UnrecognisedStorageOn(),
		PairRequests:        a.PairRequestsOn(),
		LANView:             cfg.General.LANView,
		LANViewAccess:       access,
		WebhookEnabled:      cfg.General.Webhook.Enabled,
		WebhookMinimal:      cfg.General.Webhook.Minimal,
		NtfyEnabled:         cfg.General.Ntfy.Enabled,
		NtfyMinimal:         cfg.General.Ntfy.Minimal,
	}
}

func (col *Collector) Collect() Model {
	cfg := col.Cfg()
	m := Model{
		MachineName: cfg.General.MachineName,
		Version:     version.Short(),
		Commit:      shortCommit(),
		Receive:     ReceiveInfo{Enabled: cfg.Receive.Enabled, Root: cfg.Receive.Root},
	}
	m.EngineNeeded = cfg.Receive.Enabled
	for _, t := range cfg.Targets {
		if t.Type == "device" {
			m.EngineNeeded = true
		}
	}
	m.Settings = SettingsFrom(cfg)
	if col.LANDevices != nil {
		m.Settings.LANDevices = col.LANDevices()
	}
	if col.WebhookURLSet != nil {
		m.Settings.WebhookURLSet = col.WebhookURLSet()
	}
	if col.NtfyTopicSet != nil {
		m.Settings.NtfyTopicSet = col.NtfyTopicSet()
	}
	if col.NtfyTokenSet != nil {
		m.Settings.NtfyTokenSet = col.NtfyTokenSet()
	}
	if col.NtfyTopicDisplay != nil {
		m.Settings.NtfyTopicDisplay = col.NtfyTopicDisplay()
	}
	if col.WebhookURLDisplay != nil {
		m.Settings.WebhookURLDisplay = col.WebhookURLDisplay()
	}
	if col.Delivery != nil {
		m.Settings.Delivery = col.Delivery()
	}
	if col.LANViewURL != nil {
		m.Settings.LANViewURL = col.LANViewURL()
	}
	if col.LANViewProblem != nil {
		m.Settings.LANViewError = col.LANViewProblem()
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
		var startedAt time.Time
		if conns != nil {
			if c, ok := conns.Connections[t.DeviceID]; ok {
				connected = c.Connected
				startedAt, _ = time.Parse(time.RFC3339Nano, c.StartedAt)
			}
		}
		// Reached, but never allowed to start a sync session: the other machine
		// has not approved this one. Its last-seen is no help here — a rejected
		// handshake still stamps a fresh one — so the session start is what
		// separates "waiting for approval" from "unreachable".
		awaitingPair := connected && startedAt.IsZero()
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
				case awaitingPair:
					row.State = "awaiting-pair"
				case !connected && !neverSeen(lastSeen) && time.Since(lastSeen) > staleAfter:
					// "Stale" is a backup that used to work and has now been
					// silent for too long. A machine that has never been seen
					// cannot have gone stale — it is simply not there yet.
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
			NoDefaultIgnores: f.NoDefaultIgnores,
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
			// Only a destination with storage of its own can fail to report it:
			// a paired machine has no capacity here and must never be flagged,
			// or the warning would cry wolf on a perfectly healthy target.
			info.SpaceUnknown = s.Unknown && (t.Type == "drive" || t.Type == "share")
		}
		m.Targets = append(m.Targets, info)
	}

	// The lifetime odometer, and the target mix that says what it covers.
	if col.Totals != nil {
		m.Totals.Bytes, m.Totals.Files, m.Totals.Since = col.Totals()
	}
	for _, t := range cfg.Targets {
		switch t.Type {
		case "drive", "share":
			m.Totals.MirrorTargets++
		case "device":
			m.Totals.DeviceTargets++
		}
	}

	// The wizard is owed to anyone who hasn't finished setting up. Treating
	// "has a folder AND a target" as done means a CLI-configured machine never
	// gets nagged, regardless of the flag.
	//
	// A machine that only RECEIVES backups is set up too. It has no folders and
	// no targets of its own by design, so it would otherwise be shown the
	// first-run wizard for ever — and the wizard covers the whole page, which
	// puts the "approve this machine" panel permanently out of reach on the one
	// computer that needs it.
	configured := len(cfg.Folders) > 0 && len(cfg.Targets) > 0
	flagged := col.SetupDone != nil && col.SetupDone()
	m.SetupComplete = configured || flagged || cfg.Receive.Enabled

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
			case col.HasArchivePassword != nil && !col.HasArchivePassword(job.Name):
				// Without its password the job cannot run at all — that
				// outranks every other state.
				row.NeedsPassword = true
				row.State = "needs password"
				row.Detail = "enter this snapshot's password to resume it"
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
		// Backups held for other machines. Listed even when nothing has drifted,
		// so the receiving machine can see what it is actually holding — the
		// drift count is what the UI acts on.
		if received, err := pairing.ReceivedFolders(client, cfg); err == nil {
			for _, rf := range received {
				info := ReceivedFolderInfo{ID: rf.ID, Label: rf.Label, Source: rf.Source}
				// One folder the engine won't answer for must not cost the panel
				// the others: it is listed with no drift rather than dropped.
				if fs, err := client.FolderStatus(rf.ID); err == nil {
					info.ChangedItems = fs.ReceiveOnlyTotalItems
					info.ChangedBytes = fs.ReceiveOnlyChangedBytes
				}
				m.ReceivedFolders = append(m.ReceivedFolders, info)
			}
		}
	}
	return m
}

// neverSeen reports a device syncthing holds no last-seen time for.
//
// The signal is not a Go zero time: syncthing answers /rest/stats/device with
// the unix epoch for a device it has no statistics for, and a device missing
// from that map leaves the zero value here. Both mean "never", and treating
// either as a real timestamp reads a brand-new destination as long overdue.
func neverSeen(lastSeen time.Time) bool {
	return !lastSeen.After(time.Unix(0, 0))
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
		"offline": 2, "awaiting-pair": 2, "stale": 3, "full": 4, "wrong-drive": 5, "error": 6,
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
