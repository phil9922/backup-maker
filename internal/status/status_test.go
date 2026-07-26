// SPDX-License-Identifier: MIT

package status

import (
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/phil9922/backup-maker/internal/archive"
	"github.com/phil9922/backup-maker/internal/config"
	"github.com/phil9922/backup-maker/internal/localmirror"
	"github.com/phil9922/backup-maker/internal/syncthing"
)

// The collector must fold the sampled free/total into each destination's card,
// resolve the reclaim reserve from config, and leave a paired machine (which
// owns its own storage) with no space figures at all.
func TestCollectFillsDestinationSpace(t *testing.T) {
	reserve := 20
	cfg := &config.Config{
		General:  config.General{MachineName: "workstation"},
		Defaults: config.Defaults{MinFreeGB: 5},
		Targets: []config.Target{
			{Type: "drive", Name: "sdcard", Path: "/mnt/sd", MinFreeGB: &reserve},
			{Type: "share", Name: "nas", URL: "//nas/backups"},
			{Type: "device", Name: "laptop", DeviceID: "AAAA-BBBB-CCCC"},
		},
	}

	reportedAt := time.Date(2026, 7, 24, 10, 0, 0, 0, time.UTC)
	col := &Collector{
		Cfg:     func() *config.Config { return cfg },
		Client:  func() *syncthing.Client { return nil },
		Engines: func() []*localmirror.Engine { return nil },
		Space: func() map[string]SpaceSample {
			return map[string]SpaceSample{
				"sdcard": {Free: 8 << 30, Total: 64 << 30, At: reportedAt},
				"nas":    {Free: 300 << 30, Total: 1000 << 30, At: reportedAt},
				// "laptop" deliberately absent: a paired machine never reports.
			}
		},
	}

	m := col.Collect()
	byName := map[string]TargetInfo{}
	for _, ti := range m.Targets {
		byName[ti.Name] = ti
	}

	sd := byName["sdcard"]
	if sd.FreeBytes != 8<<30 || sd.TotalBytes != 64<<30 || !sd.SpaceReportedAt.Equal(reportedAt) {
		t.Errorf("sdcard space not filled: %+v", sd)
	}
	if sd.MinFreeBytes != uint64(reserve)<<30 {
		t.Errorf("sdcard reserve = %d, want per-target override %d GB", sd.MinFreeBytes, reserve)
	}

	nas := byName["nas"]
	if nas.TotalBytes != 1000<<30 {
		t.Errorf("nas space not filled: %+v", nas)
	}
	if nas.MinFreeBytes != 5<<30 {
		t.Errorf("nas reserve = %d, want the [defaults] fallback of 5 GB", nas.MinFreeBytes)
	}

	laptop := byName["laptop"]
	if laptop.TotalBytes != 0 || laptop.FreeBytes != 0 || laptop.MinFreeBytes != 0 {
		t.Errorf("a paired machine should carry no space figures: %+v", laptop)
	}
	if !laptop.SpaceReportedAt.IsZero() {
		t.Errorf("a paired machine should have no space timestamp: %v", laptop.SpaceReportedAt)
	}
}

// A destination that CAN have free space but never reported any is unprotected
// — the reclaim reserve cannot be enforced there — and must be marked as such.
// A paired machine's blank space fields are the same shape and mean something
// completely different, so the flag must never reach one.
func TestCollectMarksDestinationsThatCannotReportSpace(t *testing.T) {
	cfg := &config.Config{
		General: config.General{MachineName: "workstation"},
		Targets: []config.Target{
			{Type: "share", Name: "nas", URL: "//nas/backups"},
			{Type: "drive", Name: "sdcard", Path: "/mnt/sd"},
			{Type: "device", Name: "laptop", DeviceID: "AAAA-BBBB-CCCC"},
		},
	}
	reportedAt := time.Date(2026, 7, 24, 10, 0, 0, 0, time.UTC)
	col := &Collector{
		Cfg:     func() *config.Config { return cfg },
		Client:  func() *syncthing.Client { return nil },
		Engines: func() []*localmirror.Engine { return nil },
		Space: func() map[string]SpaceSample {
			return map[string]SpaceSample{
				"nas":    {Unknown: true},
				"sdcard": {Free: 8 << 30, Total: 64 << 30, At: reportedAt},
				// A device could only get here by mistake; the collector must
				// refuse to flag it regardless.
				"laptop": {Unknown: true},
			}
		},
	}

	byName := map[string]TargetInfo{}
	for _, ti := range col.Collect().Targets {
		byName[ti.Name] = ti
	}
	if !byName["nas"].SpaceUnknown {
		t.Errorf("a share that never reported its space is not marked: %+v", byName["nas"])
	}
	if byName["sdcard"].SpaceUnknown {
		t.Errorf("a drive with a good reading was marked unknown: %+v", byName["sdcard"])
	}
	if byName["laptop"].SpaceUnknown {
		t.Errorf("a paired machine was marked unknown; it has no space to report: %+v", byName["laptop"])
	}
}

// A nil Space func must not panic; it simply leaves the space fields empty.
func TestCollectWithoutSpaceFunc(t *testing.T) {
	cfg := &config.Config{
		General: config.General{MachineName: "workstation"},
		Targets: []config.Target{{Type: "drive", Name: "sdcard", Path: "/mnt/sd"}},
	}
	col := &Collector{
		Cfg:     func() *config.Config { return cfg },
		Client:  func() *syncthing.Client { return nil },
		Engines: func() []*localmirror.Engine { return nil },
	}
	m := col.Collect()
	if len(m.Targets) != 1 || m.Targets[0].TotalBytes != 0 {
		t.Fatalf("expected one target with no space, got %+v", m.Targets)
	}
}

// fakeEngine stands in for our supervised syncthing: enough of the REST surface
// for the collector to build device rows. The connection entry and lastSeen are
// served verbatim, because what syncthing actually reports for an unapproved
// machine is the whole question the awaiting-pair state turns on.
func fakeEngine(t *testing.T, lastSeen string, conn map[string]any) *syncthing.Client {
	t.Helper()
	mux := http.NewServeMux()
	write := func(w http.ResponseWriter, v any) {
		_ = json.NewEncoder(w).Encode(v)
	}
	mux.HandleFunc("/rest/system/status", func(w http.ResponseWriter, r *http.Request) {
		write(w, map[string]any{"myID": "MYSELF"})
	})
	mux.HandleFunc("/rest/system/connections", func(w http.ResponseWriter, r *http.Request) {
		write(w, map[string]any{"connections": map[string]any{"REMOTE-DEVICE-ID": conn}})
	})
	mux.HandleFunc("/rest/stats/device", func(w http.ResponseWriter, r *http.Request) {
		write(w, map[string]any{"REMOTE-DEVICE-ID": map[string]any{"lastSeen": lastSeen}})
	})
	mux.HandleFunc("/rest/db/completion", func(w http.ResponseWriter, r *http.Request) {
		write(w, map[string]any{"completion": 0.0, "needItems": 3, "needBytes": 512})
	})

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	_, port, err := net.SplitHostPort(strings.TrimPrefix(srv.URL, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	n, err := strconv.Atoi(port)
	if err != nil {
		t.Fatal(err)
	}
	return syncthing.NewClient(n, "test-key")
}

func deviceCollector(t *testing.T, lastSeen string, conn map[string]any) *Collector {
	t.Helper()
	cfg := &config.Config{
		General: config.General{MachineName: "workstation"},
		Folders: []config.Folder{{ID: "f1", Label: "code", Path: "/home/alex/code"}},
		Targets: []config.Target{{Type: "device", Name: "attic-pi", DeviceID: "REMOTE-DEVICE-ID"}},
	}
	client := fakeEngine(t, lastSeen, conn)
	return &Collector{
		Cfg:     func() *config.Config { return cfg },
		Client:  func() *syncthing.Client { return client },
		Engines: func() []*localmirror.Engine { return nil },
	}
}

// A machine we can reach but that has not approved us yet.
//
// Measured against syncthing 2.1.2 while the remote genuinely had us listed as
// pending: the TLS connection is up and reported connected, but the sync
// session never starts, so startedAt stays at its zero value and no bytes move.
// The device's lastSeen is NOT a clue — a rejected handshake stamps a fresh one
// every time, which is what made an earlier reading of this state wrong.
func unapprovedConn() map[string]any {
	return map[string]any{
		"connected":               true,
		"startedAt":               "0001-01-01T00:00:00Z",
		"address":                 "192.168.1.24:22000",
		"inBytesTotal":            0,
		"outBytesTotal":           0,
		"lastConnectionDurationS": 1.8,
	}
}

func pairedConn() map[string]any {
	return map[string]any{
		"connected":     true,
		"startedAt":     time.Now().Add(-time.Hour).Format(time.RFC3339Nano),
		"address":       "192.168.1.24:22000",
		"inBytesTotal":  4096,
		"outBytesTotal": 918273,
	}
}

func offlineConn() map[string]any {
	return map[string]any{"connected": false, "startedAt": "0001-01-01T00:00:00Z"}
}

// Reachable but never allowed to sync means the other machine has not approved
// this one — the one case this state exists for. Saying "offline" (which the
// fresh lastSeen would otherwise produce) sends the user hunting a network
// fault while the real answer is a button on the other computer.
func TestCollectMarksUnapprovedDeviceAsAwaitingPair(t *testing.T) {
	fresh := time.Now().Add(-2 * time.Second).Format(time.RFC3339Nano)
	m := deviceCollector(t, fresh, unapprovedConn()).Collect()
	if len(m.Rows) != 1 {
		t.Fatalf("got %d rows, want 1", len(m.Rows))
	}
	if m.Rows[0].State != "awaiting-pair" {
		t.Errorf("row state = %q, want awaiting-pair", m.Rows[0].State)
	}
	if m.Rows[0].Stale {
		t.Error("a machine waiting to be approved was flagged stale")
	}
	if len(m.Targets) != 1 || m.Targets[0].State != "awaiting-pair" {
		t.Errorf("destination card = %+v, want awaiting-pair", m.Targets)
	}
}

// An unreachable machine is offline, whatever the reason. It cannot be waiting
// for approval: being listed as pending over there requires this machine's
// handshake to have arrived in the first place.
func TestCollectDeviceUnreachableIsOffline(t *testing.T) {
	cases := map[string]string{
		// Never seen at all. Syncthing answers with the epoch, not a zero
		// time, for a device it holds no statistics for.
		"never seen (epoch)": "1970-01-01T00:00:00Z",
		"never seen (zero)":  "0001-01-01T00:00:00Z",
		"no entry":           "",
		"seen two hours ago": time.Now().Add(-2 * time.Hour).Format(time.RFC3339Nano),
	}
	for name, lastSeen := range cases {
		t.Run(name, func(t *testing.T) {
			m := deviceCollector(t, lastSeen, offlineConn()).Collect()
			if len(m.Rows) != 1 {
				t.Fatalf("got %d rows, want 1", len(m.Rows))
			}
			if m.Rows[0].State != "offline" {
				t.Errorf("row state = %q, want offline", m.Rows[0].State)
			}
			if m.Rows[0].Stale {
				t.Error("row was flagged stale")
			}
		})
	}
}

// A destination that used to work and has now been silent for weeks is stale —
// that half of the rule has to keep working.
func TestCollectDeviceGoneQuietForWeeksIsStale(t *testing.T) {
	old := time.Now().Add(-60 * 24 * time.Hour).Format(time.RFC3339Nano)
	m := deviceCollector(t, old, offlineConn()).Collect()
	if len(m.Rows) != 1 {
		t.Fatalf("got %d rows, want 1", len(m.Rows))
	}
	if m.Rows[0].State != "stale" || !m.Rows[0].Stale {
		t.Errorf("row = %+v, want stale", m.Rows[0])
	}
}

// Once the sync session is running, the pairing is done and ordinary progress
// states take over.
func TestCollectDeviceWithALiveSessionIsSyncing(t *testing.T) {
	m := deviceCollector(t, time.Now().Format(time.RFC3339Nano), pairedConn()).Collect()
	if len(m.Rows) != 1 {
		t.Fatalf("got %d rows, want 1", len(m.Rows))
	}
	if m.Rows[0].State != "syncing" {
		t.Errorf("row state = %q, want syncing", m.Rows[0].State)
	}
}

// A machine that only receives backups has no folders and no targets of its own
// — by design. Treating that as "setup unfinished" puts the first-run wizard
// over the whole page for ever, which is exactly where the Approve button
// lives.
func TestCollectTreatsAReceiverAsSetUp(t *testing.T) {
	cfg := &config.Config{
		General: config.General{MachineName: "attic-pi"},
		Receive: config.Receive{Enabled: true, Root: "/srv/backups"},
	}
	col := &Collector{
		Cfg:     func() *config.Config { return cfg },
		Client:  func() *syncthing.Client { return nil },
		Engines: func() []*localmirror.Engine { return nil },
	}
	if !col.Collect().SetupComplete {
		t.Error("a receive-only machine was shown as needing first-run setup")
	}

	// A machine with nothing configured at all still gets the wizard.
	bare := &config.Config{General: config.General{MachineName: "fresh"}}
	col.Cfg = func() *config.Config { return bare }
	if col.Collect().SetupComplete {
		t.Error("a fresh install was marked set up; the wizard would never open")
	}
}

// A snapshot job whose password is not stored must say so — it cannot run,
// and that outranks any other state (an adoption can leave jobs like this).
func TestCollectFlagsArchiveNeedingPassword(t *testing.T) {
	cfg := &config.Config{
		General: config.General{MachineName: "workstation"},
		Targets: []config.Target{{Type: "drive", Name: "sdcard", Path: "/mnt/sd"}},
		Archives: []config.Archive{
			{Name: "weekly", Every: "weekly", Target: "sdcard", Keep: 5},
			{Name: "daily", Every: "daily", Target: "sdcard", Keep: 5},
		},
	}
	col := &Collector{
		Cfg:                func() *config.Config { return cfg },
		Client:             func() *syncthing.Client { return nil },
		Engines:            func() []*localmirror.Engine { return nil },
		Archives:           func() ([]archive.Result, map[string]time.Time) { return nil, nil },
		HasArchivePassword: func(name string) bool { return name == "weekly" },
	}
	m := col.Collect()
	if len(m.Archives) != 2 {
		t.Fatalf("got %d archive rows, want 2", len(m.Archives))
	}
	byName := map[string]ArchiveRow{}
	for _, r := range m.Archives {
		byName[r.Name] = r
	}
	if byName["weekly"].NeedsPassword || byName["weekly"].State != "never run" {
		t.Errorf("weekly (password stored) = %+v", byName["weekly"])
	}
	if !byName["daily"].NeedsPassword || byName["daily"].State != "needs password" {
		t.Errorf("daily (no password) = %+v", byName["daily"])
	}
}
