// SPDX-License-Identifier: MIT

package config

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"

	toml "github.com/pelletier/go-toml/v2"

	"github.com/phil9922/backup-maker/internal/wol"
)

type Config struct {
	General  General   `toml:"general"`
	Defaults Defaults  `toml:"defaults"`
	Folders  []Folder  `toml:"folder"`
	Targets  []Target  `toml:"target"`
	Archives []Archive `toml:"archive"`
	Receive  Receive   `toml:"receive"`
	// Retired are folders that have been stopped but whose backups are still
	// sitting on the destinations. See the Retired type for why they are
	// recorded at all.
	Retired []Retired `toml:"retired"`
}

type General struct {
	MachineName   string `toml:"machine_name"`
	DashboardPort int    `toml:"dashboard_port"`
	// LANView opts in to a SECOND, read-only dashboard on this machine's
	// network address, so a phone or another PC can watch backup progress.
	// Off by default: the dashboard is otherwise reachable only from this
	// computer, and that promise shouldn't change without being asked for.
	// Setting up or changing backups always stays loopback-only.
	LANView bool `toml:"lan_view,omitempty"`
	// LANViewPort is the port for that view. Deliberately not the dashboard
	// port, so the two listeners can never be confused.
	LANViewPort int `toml:"lan_view_port,omitempty"`
	// LANViewAccess is who on the network may read that view:
	//
	//	"all"      — anyone who can reach the address (the original behaviour)
	//	"approved" — only devices approved from the dashboard
	//
	// Empty means "all", so switching the view on behaves exactly as it did
	// before this setting existed and nobody's phone stops working on upgrade.
	//
	// APPROVAL IS KEYED TO A TOKEN THE VIEW ISSUES, not to an IP address. A
	// LAN address is a DHCP lease that moves, and is trivially spoofed by
	// anyone already on the wifi — it would gate honest devices and stop
	// nobody, which is worse than being plainly open about being open.
	LANViewAccess string `toml:"lan_view_access,omitempty"`
	// DesktopAlerts pops a desktop notification when backups stop working, and
	// again when they recover. ON by default: every other way of finding out is
	// pull-based — the dashboard and the status page only work if somebody goes
	// and looks — and silent failure is the thing this program exists to
	// prevent. A machine with no desktop (a Raspberry Pi target) has nothing to
	// notify and quietly does nothing, so the default costs it nothing.
	//
	// No omitempty: the flag must survive being written back as false, and a
	// setting that silently turns itself on again would be worse than not
	// having one.
	DesktopAlerts bool `toml:"desktop_alerts"`
	// Alerts narrows WHICH problems are announced, once DesktopAlerts has
	// decided that any are.
	Alerts AlertKinds `toml:"alerts,omitempty"`
	// Webhook posts the same alerts to a URL, so they can leave this machine.
	Webhook Webhook `toml:"webhook,omitempty"`
	// Ntfy publishes the same alerts to an ntfy topic, so a phone buzzes
	// without anything having to be run on it but the ntfy app.
	Ntfy Ntfy `toml:"ntfy,omitempty"`
	// UpdateCheck asks github.com once a day whether a newer release exists,
	// and announces it through whatever alert delivery is already switched on.
	//
	// OFF BY DEFAULT, AND THAT IS A PROMISE, NOT A PREFERENCE. The README,
	// docs/security.md and the dashboard all state that nothing reaches the
	// internet unless the user asked for it. This is the only part of the
	// program that would contact an outside service on its own, so it stays off
	// until somebody switches it on.
	//
	// IT ONLY EVER TELLS YOU. Nothing is downloaded, replaced or run — see
	// package update for why that ceiling is deliberate.
	UpdateCheck bool `toml:"update_check,omitempty"`
}

// AlertKinds switches individual alert categories off for someone who wants to
// hear about some problems and not others.
//
// EVERY FIELD IS A *bool, AND THAT IS THE WHOLE DESIGN. A plain bool defaults
// to false, so the moment this table was added, every config file already in
// existence — none of which has an [general.alerts] section — would parse as
// "every alert off" and silently stop telling anyone that their backups had
// stopped. nil means "not set", which reads as ON. It is the same tri-state
// Target.Verify uses, for the same reason.
//
// Switching a category off silences its ALL-CLEAR too. That is deliberate: the
// alerts here are sticky, and a warning that stays on screen with nothing ever
// to withdraw it sends the user to check by hand — the errand the whole feature
// exists to remove.
type AlertKinds struct {
	// BackupsStopped covers a destination that has gone stale, filled up, or
	// otherwise stopped receiving backups — and the all-clear when it resumes.
	BackupsStopped *bool `toml:"backups_stopped,omitempty"`
	// SnapshotFailed covers a scheduled snapshot that did not complete, and
	// the all-clear when a later run succeeds.
	SnapshotFailed *bool `toml:"snapshot_failed,omitempty"`
	// UnrecognisedStorage covers a reformatted card, or a stranger's disk at a
	// destination's mount point. Nothing is written to such storage — not even
	// the status page — so this alert is the only way anyone finds out.
	UnrecognisedStorage *bool `toml:"unrecognised_storage,omitempty"`
	// PairRequests covers another machine asking to back up to this one.
	// The only category here that is not a fault.
	PairRequests *bool `toml:"pair_requests,omitempty"`
}

// LANViewApprovedOnly reports whether the network view admits only devices
// that have been approved. Unset reads as "all": a setting that silently
// tightened on upgrade would cut off a phone that was working yesterday, with
// no message anywhere explaining why.
func (g General) LANViewApprovedOnly() bool { return g.LANViewAccess == "approved" }

// Webhook sends alerts to a URL of the user's choosing — a phone relay, a hub,
// a chat room. OFF BY DEFAULT and never required: backups work identically
// without it, and nothing is sent anywhere until it is switched on.
//
// THE URL ITSELF IS NOT HERE. It lives in state.json beside the share and
// archive passwords, because a webhook URL is usually a credential in its own
// right — a Slack or Discord endpoint, an ntfy topic with a token — and
// config.toml is this project's shareable file.
type Webhook struct {
	Enabled bool `toml:"enabled,omitempty"`
	// Minimal sends a content-free payload: severity and a fixed sentence, no
	// machine name, no destination names, no detail.
	//
	// The reason it exists: the last hop to a phone always crosses somebody
	// else's server, and "backups to nas-attic have been stale for 3 days"
	// describes a household to whoever runs it. Minimal mode still makes the
	// phone buzz.
	Minimal bool `toml:"minimal,omitempty"`
}

// Ntfy publishes alerts to an ntfy topic — ntfy.sh or a self-hosted server.
// OFF BY DEFAULT and never required, like every other delivery method.
//
// A SEPARATE METHOD FROM Webhook rather than a preset for it: ntfy has its own
// publish format, and going through it means the phone gets a title, a priority
// it will actually act on, and an icon, instead of a page of JSON.
//
// THE TOPIC ITSELF IS NOT HERE. It lives in state.json beside the share and
// archive passwords: on the public server a topic name is the whole of the
// access control — anyone who learns it can subscribe — and config.toml is this
// project's shareable file. The access token for a protected topic lives there
// too, for the more obvious reason.
type Ntfy struct {
	Enabled bool `toml:"enabled,omitempty"`
	// Minimal sends a content-free message: severity and a fixed sentence, no
	// machine name, no destination names, no detail. Same reasoning as the
	// webhook's, and it applies harder here — an ntfy topic is a name, not a
	// secret, and whoever guesses it reads everything sent to it.
	Minimal bool `toml:"minimal,omitempty"`
}

// on is the shared default-to-true reading of an unset category.
func on(v *bool) bool { return v == nil || *v }

func (a AlertKinds) BackupsStoppedOn() bool      { return on(a.BackupsStopped) }
func (a AlertKinds) SnapshotFailedOn() bool      { return on(a.SnapshotFailed) }
func (a AlertKinds) UnrecognisedStorageOn() bool { return on(a.UnrecognisedStorage) }
func (a AlertKinds) PairRequestsOn() bool        { return on(a.PairRequests) }

type Defaults struct {
	VersioningMaxAgeDays int      `toml:"versioning_max_age_days"`
	StaleAfterDays       int      `toml:"stale_after_days"`
	Ignore               []string `toml:"ignore"`
	// MinFreeGB keeps this much room free on every destination by deleting the
	// OLDEST BACKUP HISTORY when it runs low — old file versions and old
	// snapshots, never the live copy. 0 (the default) disables it entirely:
	// deleting a user's backups is opt-in.
	MinFreeGB int `toml:"min_free_gb"`
}

type Folder struct {
	ID               string   `toml:"id"`
	Path             string   `toml:"path"`
	Label            string   `toml:"label"`
	ExtraIgnore      []string `toml:"extra_ignore,omitempty"`
	NoDefaultIgnores bool     `toml:"no_default_ignores,omitempty"`
}

// Retired is a folder that has been stopped but whose backups are still out
// there on the destinations.
//
// WHY THIS EXISTS AT ALL. "Stop protecting" deletes nothing — that is a
// deliberate promise, not an oversight. But it also removed the folder from
// this file, and the destination's own manifest is rebuilt from the live
// folders, so it started reporting "folders": [] as well. The copy on the drive
// was then named by nothing: not the config, not the manifest, not the adopt
// flow. Gigabytes of somebody's files, reachable only by browsing the disk and
// recognising a directory name. This record is the thing that still knows.
//
// HERE RATHER THAN IN state.json, for three reasons. The daemon watches
// config.toml and reloads from it, so the whole feature rides that for free.
// The contents are user data of the same kind as [[folder]] — a path, a label,
// destination names — with no secret in them, where state.json is 0600
// machine-owned bookkeeping holding tokens and passwords. And this record is
// the only trace of an orphaned backup, so it has to be something a person can
// open, read, and delete by hand.
type Retired struct {
	// ID is the folder's ORIGINAL id, kept verbatim. A paired machine holds the
	// folder under this id, so turning the folder back on with a fresh one
	// would stand up a second copy over there and retransfer everything.
	ID               string    `toml:"id"`
	Path             string    `toml:"path"`
	Label            string    `toml:"label"`
	ExtraIgnore      []string  `toml:"extra_ignore,omitempty"`
	NoDefaultIgnores bool      `toml:"no_default_ignores,omitempty"`
	StoppedAt        time.Time `toml:"stopped_at,omitzero"`
	// MachineName as it was when the folder was stopped. The destination
	// subtree is <machine>/<label>, and a machine can be renamed by a hand edit
	// or by adopting — the name recorded here is the one the files are actually
	// filed under, which is the only one a later delete may act on.
	MachineName string `toml:"machine_name,omitempty"`
	// Copies is where a copy actually landed, RESOLVED at stop time rather than
	// copied from the literal Target.Folders lists. An empty Folders list means
	// EVERY folder, so a literal reading would record no copies at all for
	// exactly the destinations most likely to be holding one.
	Copies []RetiredCopy `toml:"copy,omitempty"`
	// Archives names the snapshot jobs this folder belonged to. Recorded for
	// honesty, never for deletion: a snapshot is one encrypted zip per run that
	// may hold other folders too, and there is no way to remove one folder from
	// inside it. Nothing in this feature ever touches them.
	Archives []RetiredArchive `toml:"archive,omitempty"`
}

// RetiredCopy is one destination's copy of a stopped folder.
type RetiredCopy struct {
	Target string `toml:"target"`
	Type   string `toml:"type"` // drive | share | device
	// Location as it was, purely so a confirmation dialog can name the place a
	// person recognises rather than a config key.
	Location string `toml:"location,omitempty"`
	// Explicit records whether the destination NAMED this folder, as opposed to
	// covering it with an empty "every folder" list. Turning the folder back on
	// must not convert an every-folder destination into an explicitly scoped
	// one — that would silently stop every folder added since from reaching it.
	Explicit bool `toml:"explicit,omitempty"`
	// DestPath and VersionsPath are the exact slash paths, relative to the
	// destination root, that this folder's live copy and version history
	// occupy. Recorded rather than recomputed later, because the label and the
	// machine name can both change in between — and because a recursive delete
	// should aim at a path written down when it was known to be right.
	DestPath     string `toml:"dest_path,omitempty"`
	VersionsPath string `toml:"versions_path,omitempty"`
	// Removed and Error carry the outcome of a delete attempt, so a destination
	// that was unplugged when the button was pressed is REPORTED rather than
	// quietly counted as done and forgotten.
	Removed bool   `toml:"removed,omitempty"`
	Error   string `toml:"error,omitempty"`
}

// RetiredArchive is one snapshot job a stopped folder belonged to.
type RetiredArchive struct {
	Name     string `toml:"name"`
	Explicit bool   `toml:"explicit,omitempty"`
}

type Target struct {
	// Type is "device" (LAN machine running backup-maker), "drive" (locally
	// attached storage), or "share" (SMB network share — NAS, router USB
	// drive, or a folder shared by any computer).
	Type     string `toml:"type"`
	Name     string `toml:"name"`
	DeviceID string `toml:"device_id,omitempty"`
	Path     string `toml:"path,omitempty"`
	// URL locates a share target: //host[:port]/share[/subpath].
	URL string `toml:"url,omitempty"`
	// Username for the share; the password lives in the private state.json,
	// never in this (shareable) file.
	Username string `toml:"username,omitempty"`
	// Verify re-reads every written file and compares checksums. nil means
	// the type default: on for shares, off for drives.
	Verify *bool `toml:"verify,omitempty"`
	// MAC is the target machine's network adapter address. Setting it turns
	// on Wake-on-LAN: while the target is offline the daemon broadcasts a
	// magic packet to try to wake it. Only meaningful for "share" and
	// "device" targets (a "drive" is attached to this machine). Waking is
	// best-effort — see docs; an unset MAC disables it entirely.
	MAC string `toml:"mac,omitempty"`
	// WakeBroadcast overrides the broadcast address used for this target's
	// magic packets. Normally empty: backup-maker broadcasts on every local
	// subnet. Set it when the target sits on a subnet this machine can't
	// enumerate (e.g. "192.168.1.255").
	WakeBroadcast string `toml:"wake_broadcast,omitempty"`
	// ArchivesOnly marks a destination that receives scheduled snapshots but
	// no continuous mirror. Without it there would be no way to express
	// "timed backups here, nothing live": an empty Folders list already means
	// *every* folder, so a snapshot-only destination would silently start
	// mirroring everything.
	ArchivesOnly bool `toml:"archives_only,omitempty"`
	// MinFreeGB overrides [defaults].min_free_gb for this destination — a
	// 64GB card and a 4TB NAS want different headroom. Pointer so "not set"
	// (inherit) is distinguishable from "explicitly 0" (off).
	MinFreeGB *int `toml:"min_free_gb,omitempty"`
	// Folders lists folder IDs backed up to this target; empty means all.
	Folders []string `toml:"folders"`
}

// MinFreeBytes resolves the reclaim threshold for a target, falling back to
// the global default. Returns 0 when reclaiming is off.
func (c *Config) MinFreeBytes(t Target) uint64 {
	// A paired machine runs its own backup-maker and owns its storage; we
	// cannot enumerate or delete over there, so the setting is meaningless.
	if t.Type == "device" {
		return 0
	}
	gb := c.Defaults.MinFreeGB
	if t.MinFreeGB != nil {
		gb = *t.MinFreeGB
	}
	if gb <= 0 {
		return 0
	}
	return uint64(gb) << 30
}

// LANViewPort resolves the read-only view's port, falling back to the default.
func (c *Config) LANViewPort() int {
	if c.General.LANViewPort > 0 {
		return c.General.LANViewPort
	}
	return DefaultLANViewPort
}

// WakeEnabled reports whether Wake-on-LAN is configured for this target.
func (t Target) WakeEnabled() bool { return t.MAC != "" && t.Type != "drive" }

// Archive is a scheduled snapshot: an AES-256 password-protected zip of
// the selected folders, written to a drive/share target on a timer. Distinct
// from the real-time mirror — an archive is a frozen snapshot. The password
// is REQUIRED and lives in the private state.json, never here.
type Archive struct {
	Name string `toml:"name"`
	// Folders lists folder IDs to include; empty means all folders.
	Folders []string `toml:"folders"`
	// Every is the schedule: a Go duration ("30m", "12h") or "daily"/"weekly".
	Every string `toml:"every"`
	// Target names the drive/share target the archives are written to.
	Target string `toml:"target"`
	// Keep is how many archives to retain (oldest pruned). Default 5.
	Keep int `toml:"keep"`
	// NoDefaultIgnores makes this snapshot include the junk the mirror skips
	// — node_modules, build output, caches. Exists because the ignore list is
	// otherwise shared between the mirror and the snapshot, so there would be
	// no way to keep a small live copy on an SD card while still sealing a
	// complete archive on a bigger drive.
	NoDefaultIgnores bool `toml:"no_default_ignores,omitempty"`
	// ExtraIgnore adds patterns for this snapshot only, on top of whatever
	// the folder already excludes.
	ExtraIgnore []string `toml:"extra_ignore,omitempty"`
	// Paused stops the schedule running without throwing it away — the
	// password, the retention count and the folder it covers all survive.
	//
	// A SEPARATE THING FROM DELETING IT, because the two answer different
	// questions. "Stop this for now" is reversible and keeps what you set up;
	// "I do not want this job" is not. Without a pause, the only way to stop a
	// schedule was to delete it and build it again from scratch — including
	// retyping a password that by design cannot be recovered.
	Paused bool `toml:"paused,omitempty"`
}

type Receive struct {
	Enabled         bool     `toml:"enabled"`
	Root            string   `toml:"root"`
	AcceptedSources []string `toml:"accepted_sources"`
}

// New returns a Config populated with defaults for this machine.
func New() *Config {
	host, err := os.Hostname()
	if err != nil || host == "" {
		host = "my-machine"
	}
	return &Config{
		General: General{
			MachineName:   host,
			DashboardPort: DefaultDashboardPort,
			DesktopAlerts: true,
		},
		Defaults: Defaults{
			VersioningMaxAgeDays: DefaultVersioningMaxDays,
			StaleAfterDays:       DefaultStaleAfterDays,
			Ignore:               append([]string(nil), DefaultIgnores...),
		},
		Receive: Receive{AcceptedSources: []string{}},
	}
}

func Load() (*Config, error) {
	path, err := ConfigPath()
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	cfg := New()
	if err := toml.Unmarshal(data, cfg); err != nil {
		return nil, fmt.Errorf("parsing %s: %w", path, err)
	}
	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("invalid config %s: %w", path, err)
	}
	return cfg, nil
}

// Exists reports whether a config file has been created.
func Exists() bool {
	path, err := ConfigPath()
	if err != nil {
		return false
	}
	_, err = os.Stat(path)
	return err == nil
}

func (c *Config) Save() error {
	if err := c.Validate(); err != nil {
		return err
	}
	path, err := ConfigPath()
	if err != nil {
		return err
	}
	data, err := toml.Marshal(c)
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func (c *Config) Validate() error {
	var errs []error
	if c.General.DashboardPort < 1 || c.General.DashboardPort > 65535 {
		errs = append(errs, fmt.Errorf("general.dashboard_port %d out of range", c.General.DashboardPort))
	}
	if c.General.LANView {
		port := c.LANViewPort()
		if port < 1 || port > 65535 {
			errs = append(errs, fmt.Errorf("general.lan_view_port %d out of range", port))
		}
		if port == c.General.DashboardPort {
			errs = append(errs, fmt.Errorf("general.lan_view_port must differ from dashboard_port (%d): the read-only network view and the full dashboard are separate listeners", port))
		}
	}
	seen := map[string]bool{}
	// THE LABEL IS THE DIRECTORY, so two folders sharing one is two mirrors
	// writing to one place, each versioning away what the other just wrote.
	// Compared on the sanitized destination rather than the label itself:
	// "a/b" and "a_b" are different labels that land in exactly the same
	// directory, and comparing the raw strings would wave that pair through.
	//
	// Checked here as well as in setup.AppendFolder because config.toml is a
	// file people edit, and this is the reading that a hand-written collision
	// has to get past.
	dest := map[string]string{}
	for i, f := range c.Folders {
		if f.ID == "" {
			errs = append(errs, fmt.Errorf("folder[%d] (%s) missing id", i, f.Path))
		} else if seen[f.ID] {
			errs = append(errs, fmt.Errorf("duplicate folder id %q", f.ID))
		}
		seen[f.ID] = true
		if !filepath.IsAbs(f.Path) {
			errs = append(errs, fmt.Errorf("folder %q path must be absolute: %s", f.ID, f.Path))
		}
		if f.Label != "" {
			// Keyed case-folded: the destination filesystem usually is.
			d := strings.ToLower(DestRoot(c.General.MachineName, f.Label))
			if other, clash := dest[d]; clash {
				errs = append(errs, fmt.Errorf(
					"folders %q and %q both back up into %q — they would delete each other's files; give one of them a different label",
					other, f.Label, d))
			}
			dest[d] = f.Label
		}
	}
	stopped := map[string]bool{}
	for _, r := range c.Retired {
		stopped[r.ID] = true
	}
	for i, t := range c.Targets {
		switch t.Type {
		case "device":
			if t.DeviceID == "" {
				errs = append(errs, fmt.Errorf("target[%d] (%s) type=device missing device_id", i, t.Name))
			}
		case "drive":
			if !filepath.IsAbs(t.Path) {
				errs = append(errs, fmt.Errorf("target[%d] (%s) path must be absolute: %s", i, t.Name, t.Path))
			}
		case "share":
			if t.URL == "" {
				errs = append(errs, fmt.Errorf("target[%d] (%s) type=share missing url (//host/share)", i, t.Name))
			}
			if t.Path != "" {
				errs = append(errs, fmt.Errorf("target[%d] (%s) type=share takes url, not path", i, t.Name))
			}
		default:
			errs = append(errs, fmt.Errorf("target[%d] (%s) unknown type %q", i, t.Name, t.Type))
		}
		if t.MinFreeGB != nil {
			if *t.MinFreeGB < 0 {
				errs = append(errs, fmt.Errorf("target[%d] (%s) min_free_gb cannot be negative", i, t.Name))
			}
			if t.Type == "device" {
				errs = append(errs, fmt.Errorf("target[%d] (%s) type=device cannot use min_free_gb: that machine manages its own storage", i, t.Name))
			}
		}
		if t.ArchivesOnly && t.Type == "device" {
			errs = append(errs, fmt.Errorf("target[%d] (%s) type=device cannot be archives_only: a paired machine receives a live mirror, not snapshot files", i, t.Name))
		}
		if t.MAC != "" {
			if _, err := wol.ParseMAC(t.MAC); err != nil {
				errs = append(errs, fmt.Errorf("target[%d] (%s) mac: %w", i, t.Name, err))
			}
			if t.Type == "drive" {
				errs = append(errs, fmt.Errorf("target[%d] (%s) type=drive cannot use mac: a drive attached to this computer has nothing to wake", i, t.Name))
			}
		}
		if t.WakeBroadcast != "" {
			if t.MAC == "" {
				errs = append(errs, fmt.Errorf("target[%d] (%s) wake_broadcast set without mac", i, t.Name))
			}
			if ip := net.ParseIP(t.WakeBroadcast); ip == nil || ip.To4() == nil {
				errs = append(errs, fmt.Errorf("target[%d] (%s) wake_broadcast %q is not an IPv4 address", i, t.Name, t.WakeBroadcast))
			}
		}
		for _, fid := range t.Folders {
			if !seen[fid] && !stopped[fid] {
				errs = append(errs, fmt.Errorf("target %q references unknown folder id %q", t.Name, fid))
			}
		}
	}
	// A STOPPED folder's id is still a known id. Targets and archive jobs keep
	// naming it while its retired record exists, which is what stops an empty
	// list from meaning "every folder" and silently re-aiming a snapshot
	// schedule at whatever else is configured. See setup.RemoveFolder.
	targetType := map[string]string{}
	for _, t := range c.Targets {
		targetType[t.Name] = t.Type
	}
	seenArchive := map[string]bool{}
	for i, a := range c.Archives {
		if a.Name == "" {
			errs = append(errs, fmt.Errorf("archive[%d] missing name", i))
		} else if seenArchive[a.Name] {
			errs = append(errs, fmt.Errorf("duplicate archive name %q", a.Name))
		}
		seenArchive[a.Name] = true
		if _, err := ParseEvery(a.Every); err != nil {
			errs = append(errs, fmt.Errorf("archive %q: %w", a.Name, err))
		}
		switch targetType[a.Target] {
		case "drive", "share":
		case "device":
			errs = append(errs, fmt.Errorf("archive %q: target %q is a paired machine; archives need a drive or network-drive target", a.Name, a.Target))
		default:
			errs = append(errs, fmt.Errorf("archive %q references unknown target %q", a.Name, a.Target))
		}
		for _, fid := range a.Folders {
			if !seen[fid] && !stopped[fid] {
				errs = append(errs, fmt.Errorf("archive %q references unknown folder id %q", a.Name, fid))
			}
		}
		if a.Keep < 0 {
			errs = append(errs, fmt.Errorf("archive %q: keep must be >= 1 (or omitted for the default)", a.Name))
		}
	}
	if c.Receive.Enabled && !filepath.IsAbs(c.Receive.Root) {
		errs = append(errs, errors.New("receive.root must be an absolute path when receive.enabled"))
	}
	return errors.Join(errs...)
}

// DefaultArchiveKeep is how many archives are retained when keep is unset.
const DefaultArchiveKeep = 5

// ParseEvery converts an archive schedule ("30m", "12h", "daily", "weekly")
// into a duration. Minimum 5 minutes.
func ParseEvery(s string) (time.Duration, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "":
		return 0, errors.New("missing schedule (e.g. \"12h\", \"daily\", \"weekly\")")
	case "hourly":
		return time.Hour, nil
	case "daily":
		return 24 * time.Hour, nil
	case "weekly":
		return 7 * 24 * time.Hour, nil
	}
	d, err := time.ParseDuration(s)
	if err != nil {
		return 0, fmt.Errorf("bad schedule %q (use \"30m\", \"12h\", \"daily\", \"weekly\")", s)
	}
	if d < 5*time.Minute {
		return 0, fmt.Errorf("schedule %q too frequent (minimum 5m)", s)
	}
	return d, nil
}

// FoldersForArchive resolves the folder set an archive includes (empty list
// means every configured folder).
func (c *Config) FoldersForArchive(a Archive) []Folder {
	if len(a.Folders) == 0 {
		return c.Folders
	}
	want := map[string]bool{}
	for _, id := range a.Folders {
		want[id] = true
	}
	var out []Folder
	for _, f := range c.Folders {
		if want[f.ID] {
			out = append(out, f)
		}
	}
	return out
}

// FoldersForTarget resolves the folder list a target mirrors continuously
// (empty list in config means every configured folder).
//
// A snapshot-only destination mirrors nothing, so this is the single choke
// point that keeps it out of the mirror engines, the status rows, and the
// syncthing reconciler alike.
func (c *Config) FoldersForTarget(t Target) []Folder {
	if t.ArchivesOnly {
		return nil
	}
	if len(t.Folders) == 0 {
		return c.Folders
	}
	want := map[string]bool{}
	for _, id := range t.Folders {
		want[id] = true
	}
	var out []Folder
	for _, f := range c.Folders {
		if want[f.ID] {
			out = append(out, f)
		}
	}
	return out
}

// Configured reports that this machine has evidently been set up: it protects
// something of its own, or it exists to receive backups for somebody else.
//
// ONE DEFINITION RATHER THAN THE EXPRESSION WRITTEN OUT AT EACH CALL SITE. Two
// readings of "is this machine set up" that disagree is exactly how the
// dashboard came to throw a first-run wizard over a working install: status
// derived the answer from the live config, the daemon only ever wrote the flag
// from the browser wizard, and removing the last folder made the two disagree.
//
// NOTE WHAT THIS IS NOT: it is a statement about the config in front of it, and
// therefore not monotonic — emptying a config makes it false again. Anything
// that needs "has this machine EVER been set up" must consult the persisted
// State.SetupComplete, which is what this predicate is used to set.
func (c *Config) Configured() bool {
	return (len(c.Folders) > 0 && len(c.Targets) > 0) || c.Receive.Enabled
}

// NewFolderID generates a short random, stable folder identifier such as
// "kqz3d-8xh2p". It is shared verbatim with target machines.
func NewFolderID() string {
	const alphabet = "abcdefghijklmnopqrstuvwxyz0123456789"
	b := make([]byte, 10)
	if _, err := rand.Read(b); err != nil {
		panic(err) // crypto/rand failure is unrecoverable
	}
	var sb strings.Builder
	for i, v := range b {
		if i == 5 {
			sb.WriteByte('-')
		}
		sb.WriteByte(alphabet[int(v)%len(alphabet)])
	}
	return sb.String()
}

// NewToken returns a random hex token (API keys, IPC auth).
func NewToken() string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}
