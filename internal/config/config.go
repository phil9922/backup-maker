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
}

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
			if !seen[fid] {
				errs = append(errs, fmt.Errorf("target %q references unknown folder id %q", t.Name, fid))
			}
		}
	}
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
			if !seen[fid] {
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
