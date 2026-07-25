// SPDX-License-Identifier: MIT

package daemon

import (
	"errors"
	"testing"
	"time"
)

// fakeReporter is a SpaceReporter whose answer we can change between calls.
type fakeReporter struct {
	free, total uint64
	err         error
}

func (f *fakeReporter) Usage() (uint64, uint64, error) { return f.free, f.total, f.err }

// A destination that stops answering must keep its last good reading, not lose
// the bar: a NAS that naps between samples is the normal case, not an error.
func TestRecordSampleKeepsLastKnownGood(t *testing.T) {
	d := &daemon{}
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

	// A zero total (some backends' way of saying "can't tell") is also ignored.
	r.err = nil
	r.total = 0
	d.recordSample("nas", r, t0.Add(2*time.Minute))
	if got := d.spaceSamples()["nas"]; got.Total != 1000 {
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

// A destination that has never answered has no entry at all, so its card shows
// no bar rather than a fabricated zero.
func TestSpaceSamplesEmptyWhenNothingSampled(t *testing.T) {
	d := &daemon{}
	if got := d.spaceSamples(); len(got) != 0 {
		t.Fatalf("expected no samples, got %+v", got)
	}
}
