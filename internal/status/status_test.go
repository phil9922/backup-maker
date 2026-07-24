// SPDX-License-Identifier: MIT

package status

import (
	"testing"
	"time"

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
