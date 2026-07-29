// SPDX-License-Identifier: MIT

package daemon

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/phil9922/backup-maker/internal/config"
	"github.com/phil9922/backup-maker/internal/localmirror"
	"github.com/phil9922/backup-maker/internal/status"
	"github.com/phil9922/backup-maker/internal/statuspage"
	"github.com/phil9922/backup-maker/internal/syncthing"
)

// fakeReporter is a SpaceReporter whose answer we can change between calls.
type fakeReporter struct {
	free, total uint64
	err         error
}

func (f *fakeReporter) Usage() (uint64, uint64, error) { return f.free, f.total, f.err }

// testDaemon is a daemon with just enough wired up to sample space: recordSample
// logs, so it needs a logger even when the output goes nowhere.
func testDaemon() *daemon {
	return &daemon{log: slog.New(slog.NewTextHandler(io.Discard, nil))}
}

// A destination that stops answering must keep its last good reading, not lose
// the bar: a NAS that naps between samples is the normal case, not an error.
func TestRecordSampleKeepsLastKnownGood(t *testing.T) {
	d := testDaemon()
	r := &fakeReporter{free: 300, total: 1000}

	t0 := time.Date(2026, 7, 24, 10, 0, 0, 0, time.UTC)
	d.recordSample("nas", r, t0)

	got := d.spaceSamples()["nas"]
	if got.Free != 300 || got.Total != 1000 || !got.At.Equal(t0) {
		t.Fatalf("first sample not cached: %+v", got)
	}

	// The destination goes offline: the read errors.
	r.err = errors.New("host is down")
	d.recordSample("nas", r, t0.Add(time.Minute))

	got = d.spaceSamples()["nas"]
	if got.Free != 300 || got.Total != 1000 || !got.At.Equal(t0) {
		t.Fatalf("a failed read overwrote the last-known-good value: %+v", got)
	}
	// And it stays a real reading: a destination that answered once is quiet,
	// not unmeasurable, so flagging it would cry wolf every time a NAS naps.
	if got.Unknown {
		t.Fatalf("a destination that has answered before was marked unknown: %+v", got)
	}

	// A zero total (some backends' way of saying "can't tell") is also ignored.
	r.err = nil
	r.total = 0
	d.recordSample("nas", r, t0.Add(2*time.Minute))
	if got := d.spaceSamples()["nas"]; got.Total != 1000 || got.Unknown {
		t.Fatalf("a zero-total read overwrote the cache: %+v", got)
	}

	// A later good read does update, timestamp and all.
	r.free, r.total = 250, 1000
	t1 := t0.Add(3 * time.Minute)
	d.recordSample("nas", r, t1)
	if got := d.spaceSamples()["nas"]; got.Free != 250 || !got.At.Equal(t1) {
		t.Fatalf("a fresh read did not update the cache: %+v", got)
	}
}

// A destination that has never been sampled at all has no entry, so its card
// shows no bar rather than a fabricated zero — and no warning either, because
// nothing has gone wrong yet.
func TestSpaceSamplesEmptyWhenNothingSampled(t *testing.T) {
	d := testDaemon()
	if got := d.spaceSamples(); len(got) != 0 {
		t.Fatalf("expected no samples, got %+v", got)
	}
}

// THE BUG THIS GUARDS: some NAS firmware never answers the filesystem-info
// query, so free space can never be read there. The error used to be dropped on
// the floor, leaving the destination indistinguishable from a healthy one while
// the min_free_gb reserve went quietly unenforced.
func TestRecordSampleMarksNeverMeasuredDestinationUnknown(t *testing.T) {
	d := testDaemon()
	r := &fakeReporter{err: errors.New("unsupported information class")}

	t0 := time.Date(2026, 7, 24, 10, 0, 0, 0, time.UTC)
	d.recordSample("nas", r, t0)
	d.recordSample("nas", r, t0.Add(time.Minute))

	got, ok := d.spaceSamples()["nas"]
	if !ok {
		t.Fatal("a destination that can never report its free space was dropped silently")
	}
	if !got.Unknown {
		t.Errorf("destination not marked unknown: %+v", got)
	}
	if got.Total != 0 || got.Free != 0 {
		t.Errorf("an unmeasurable destination invented figures: %+v", got)
	}
	// No fabricated timestamp: nothing was ever reported, so "as of ..." would
	// be a lie the UI would happily print.
	if !got.At.IsZero() {
		t.Errorf("unknown sample carries a report time: %v", got.At)
	}

	// A destination that starts answering clears the flag.
	r.err, r.free, r.total = nil, 300, 1000
	t1 := t0.Add(2 * time.Minute)
	d.recordSample("nas", r, t1)
	if got := d.spaceSamples()["nas"]; got.Unknown || got.Total != 1000 || !got.At.Equal(t1) {
		t.Errorf("a destination that came good is still marked unknown: %+v", got)
	}
}

// countingHandler counts log records at or above a level, so the rate-limiting
// choice can be asserted rather than assumed.
type countingHandler struct {
	slog.Handler
	warns int
}

func (h *countingHandler) Enabled(context.Context, slog.Level) bool { return true }
func (h *countingHandler) Handle(_ context.Context, r slog.Record) error {
	if r.Level >= slog.LevelWarn {
		h.warns++
	}
	return nil
}
func (h *countingHandler) WithAttrs([]slog.Attr) slog.Handler { return h }
func (h *countingHandler) WithGroup(string) slog.Handler      { return h }

// The failure is logged on the way in and then not again: this runs once a
// minute forever, and a warning per minute for an unreachable NAS is noise that
// buries everything else in the log.
func TestRecordSampleWarnsOnceAboutAnUnmeasurableDestination(t *testing.T) {
	h := &countingHandler{}
	d := &daemon{log: slog.New(h)}
	r := &fakeReporter{err: errors.New("unsupported information class")}

	t0 := time.Date(2026, 7, 24, 10, 0, 0, 0, time.UTC)
	for i := range 5 {
		d.recordSample("nas", r, t0.Add(time.Duration(i)*time.Minute))
	}
	if h.warns != 1 {
		t.Fatalf("logged %d warnings for one failing destination, want exactly 1", h.warns)
	}

	// A destination that goes quiet after answering is the normal case, and must
	// not raise a warning at all.
	h.warns = 0
	good := &fakeReporter{free: 300, total: 1000}
	d.recordSample("sdcard", good, t0)
	good.err = errors.New("host is down")
	d.recordSample("sdcard", good, t0.Add(time.Minute))
	if h.warns != 0 {
		t.Fatalf("warned %d times about a destination that simply went quiet", h.warns)
	}
}

// End to end from the sampler to the dashboard model: only destinations that
// HAVE storage of their own can fail to report it. A paired machine has no
// capacity here, so it must never carry the warning.
func TestUnknownSpaceReachesTheModelButNeverAPairedMachine(t *testing.T) {
	d := testDaemon()
	r := &fakeReporter{err: errors.New("unsupported information class")}
	t0 := time.Date(2026, 7, 24, 10, 0, 0, 0, time.UTC)
	d.recordSample("nas", r, t0)
	// A paired machine is never sampled (it has no backend here), but pretend
	// something did mark it, so the guard itself is under test.
	d.recordSample("laptop", r, t0)

	cfg := &config.Config{
		General: config.General{MachineName: "workstation"},
		Targets: []config.Target{
			{Type: "share", Name: "nas", URL: "//nas/backups"},
			{Type: "device", Name: "laptop", DeviceID: "AAAA-BBBB-CCCC"},
		},
	}
	col := &status.Collector{
		Cfg:     func() *config.Config { return cfg },
		Client:  func() *syncthing.Client { return nil },
		Engines: func() []*localmirror.Engine { return nil },
		Space:   d.spaceSamples,
	}
	byName := map[string]status.TargetInfo{}
	for _, ti := range col.Collect().Targets {
		byName[ti.Name] = ti
	}
	if !byName["nas"].SpaceUnknown {
		t.Errorf("the share's unmeasurable space never reached the model: %+v", byName["nas"])
	}
	if byName["laptop"].SpaceUnknown {
		t.Errorf("a paired machine was flagged as missing free space: %+v", byName["laptop"])
	}
}

// The on-destination status page has the same duty as the dashboard: a
// destination that cannot be measured gets a line saying so, not no line.
func TestBuildPageShowsUnmeasurableDestinations(t *testing.T) {
	now := time.Date(2026, 7, 24, 10, 0, 0, 0, time.UTC)
	p, _ := buildPage(status.Model{
		MachineName: "workstation",
		Targets: []status.TargetInfo{
			{Name: "nas", Type: "share", SpaceUnknown: true},
			{Name: "sdcard", Type: "drive", FreeBytes: 8 << 30, TotalBytes: 64 << 30},
			{Name: "laptop", Type: "device"}, // no storage of its own; stays out
		},
	}, now)

	byName := map[string]statuspage.StorageLine{}
	for _, s := range p.Storage {
		byName[s.Destination] = s
	}
	if len(p.Storage) != 2 {
		t.Fatalf("expected the share and the drive only, got %+v", p.Storage)
	}
	if !byName["nas"].Unavailable {
		t.Errorf("the unmeasurable share is missing or looks healthy: %+v", byName["nas"])
	}
	if byName["sdcard"].Unavailable || byName["sdcard"].Free != "8GB" {
		t.Errorf("a measured drive was mangled: %+v", byName["sdcard"])
	}
}
