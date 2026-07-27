// SPDX-License-Identifier: MIT

package watchdog

import (
	"context"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"
)

func quietLog() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// fakeNotifier records keepalives instead of sending them.
type fakeNotifier struct {
	mu    sync.Mutex
	pings int
}

func (f *fakeNotifier) Ping() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.pings++
	return nil
}

func (f *fakeNotifier) Close() error { return nil }

func (f *fakeNotifier) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.pings
}

// THE DECISION, in isolation. Everything that could kill a healthy daemon is
// decided here, so this is where it is pinned down.
func TestTrackerToleratesABriefLockHold(t *testing.T) {
	base := time.Date(2026, 7, 26, 21, 0, 0, 0, time.UTC)
	tr := tracker{grace: 90 * time.Second}

	if !tr.observe(true, base) {
		t.Fatal("a free lock did not count as alive")
	}
	// A config apply holds the lock across a few probes. That is normal work,
	// not a wedge: the pings must continue.
	for i := 1; i <= 4; i++ {
		at := base.Add(time.Duration(i) * 10 * time.Second)
		if !tr.observe(false, at) {
			t.Fatalf("stopped pinging after only %v of lock unavailability", at.Sub(base))
		}
	}
	// And the clock resets when the apply finishes.
	if !tr.observe(true, base.Add(50*time.Second)) {
		t.Fatal("a lock that came back did not count as alive")
	}
	if !tr.stuckSince.IsZero() {
		t.Error("recovery left the stuck-since clock running")
	}
}

func TestTrackerStopsAfterSustainedUnavailability(t *testing.T) {
	base := time.Date(2026, 7, 26, 21, 0, 0, 0, time.UTC)
	tr := tracker{grace: 90 * time.Second}
	tr.observe(true, base)

	// Right up to the grace period, we are still saying nothing is wrong.
	if !tr.observe(false, base.Add(10*time.Second)) {
		t.Fatal("gave up on the first failed probe")
	}
	if !tr.observe(false, base.Add(100*time.Second)) { // 90s stuck: exactly grace
		t.Fatal("gave up at the grace period rather than past it")
	}
	// Past it, the daemon is wedged and systemd should be allowed to act.
	if tr.observe(false, base.Add(101*time.Second)) {
		t.Fatal("kept claiming to be alive after grace had elapsed")
	}
	// Still wedged, still silent.
	if tr.observe(false, base.Add(5*time.Minute)) {
		t.Fatal("kept claiming to be alive well past grace")
	}
	// A daemon that frees the lock again is alive again, and a later hold gets
	// a fresh grace period rather than inheriting the old one.
	if !tr.observe(true, base.Add(6*time.Minute)) {
		t.Fatal("did not recover when the lock freed up")
	}
	if !tr.observe(false, base.Add(6*time.Minute+time.Second)) {
		t.Fatal("a fresh hold inherited the previous run's stuck clock")
	}
}

// Several probes must fit in every watchdog window, so one probe landing inside
// a legitimate lock hold can never be the reason a deadline is missed.
func TestProbeEveryFitsSeveralPingsPerWindow(t *testing.T) {
	cases := []struct {
		interval time.Duration
		want     time.Duration
	}{
		{180 * time.Second, 10 * time.Second}, // what the installed unit asks for
		{60 * time.Second, 10 * time.Second},
		{30 * time.Second, 7500 * time.Millisecond}, // short window: cadence follows it down
		{time.Second, 250 * time.Millisecond},
		{100 * time.Millisecond, 100 * time.Millisecond}, // floor, not a spin loop
	}
	for _, c := range cases {
		got := probeEvery(c.interval)
		if got != c.want {
			t.Errorf("probeEvery(%v) = %v, want %v", c.interval, got, c.want)
		}
		// The property behind the numbers, for any interval long enough to
		// have room for it: several probes per window, so one missed probe is
		// never one missed deadline.
		if c.interval > 4*fastestProbe && got > c.interval/4 {
			t.Errorf("probeEvery(%v) = %v: too slow to fit four probes in a window", c.interval, got)
		}
	}
}

// driver runs the loop under test and drives it one tick at a time, with no
// sleeping and nothing left in flight.
//
// The probe is the barrier. The loop calls it immediately after taking a tick
// and blocks there until the test answers, so when tick() returns, EVERY
// EARLIER TICK HAS BEEN FULLY ACTED ON and no later keepalive can have slipped
// in yet. The keepalive count is exactly settled at that instant.
type driver struct {
	t       *testing.T
	n       *fakeNotifier
	ticks   chan time.Time
	answers chan bool
	done    chan struct{}
	cancel  context.CancelFunc
}

func newDriver(t *testing.T, grace time.Duration) *driver {
	t.Helper()
	d := &driver{
		t:       t,
		n:       &fakeNotifier{},
		ticks:   make(chan time.Time),
		answers: make(chan bool),
		done:    make(chan struct{}),
	}
	ctx, cancel := context.WithCancel(context.Background())
	d.cancel = cancel
	go func() {
		defer close(d.done)
		pinger(ctx, d.n, func() bool { return <-d.answers }, grace, d.ticks, quietLog())
	}()
	return d
}

func (d *driver) tick(at time.Time) {
	d.t.Helper()
	select {
	case d.ticks <- at:
	case <-time.After(5 * time.Second):
		d.t.Fatal("the watchdog loop stopped reading ticks")
	}
}

// answer releases the probe the loop is waiting in.
func (d *driver) answer(lockFree bool) {
	d.t.Helper()
	select {
	case d.answers <- lockFree:
	case <-time.After(5 * time.Second):
		d.t.Fatal("the watchdog loop never probed for liveness")
	}
}

func (d *driver) stop() {
	d.t.Helper()
	d.cancel()
	select {
	case <-d.done:
	case <-time.After(5 * time.Second):
		d.t.Fatal("the watchdog loop ignored a cancelled context")
	}
}

// The whole behaviour end to end: keepalives while the lock is free, keepalives
// straight through a brief hold, silence once a hold outlasts grace, and
// keepalives again the moment the lock comes back.
func TestPingerStopsAndResumesWithTheLock(t *testing.T) {
	d := newDriver(t, 30*time.Second)
	base := time.Date(2026, 7, 26, 21, 0, 0, 0, time.UTC)
	at := func(sec int) time.Time { return base.Add(time.Duration(sec) * time.Second) }

	// Healthy: one keepalive per tick.
	for _, s := range []int{0, 10, 20} {
		d.tick(at(s))
		d.answer(true)
	}
	d.tick(at(30))
	if got := d.n.count(); got != 3 {
		t.Fatalf("a healthy daemon sent %d keepalives in 3 ticks, want 3", got)
	}

	// The lock goes to a config apply at t=30 and is held. Inside grace this
	// is ordinary work, and the keepalives must not falter.
	d.answer(false)
	for _, s := range []int{40, 50} {
		d.tick(at(s))
		d.answer(false)
	}
	d.tick(at(60))
	if got := d.n.count(); got != 6 {
		t.Fatalf("sent %d keepalives across a 20s lock hold, want 6 — a brief hold must not look like a deadlock", got)
	}

	// Exactly at grace it is still given the benefit of the doubt; past it,
	// this is a deadlock and systemd must be allowed to act.
	d.answer(false) // t=60: stuck 30s, exactly grace — still alive
	for _, s := range []int{70, 80} {
		d.tick(at(s))
		d.answer(false)
	}
	d.tick(at(90))
	if got := d.n.count(); got != 7 {
		t.Fatalf("keepalive count %d, want 7: the ping at grace should have been the last one", got)
	}

	// Recovery. Whatever it was, it finished; the daemon is alive.
	d.answer(false)
	d.tick(at(100))
	d.answer(true)
	d.tick(at(110))
	if got := d.n.count(); got != 8 {
		t.Fatalf("keepalive count %d, want 8: keepalives must resume as soon as the lock frees up", got)
	}
	d.answer(true)

	d.stop()
}

// The loop on a real ticker, with the cadence Run would derive: a healthy
// daemon reports in several times per watchdog window, not once.
func TestPingerKeepsCadenceOnARealTicker(t *testing.T) {
	const interval = 400 * time.Millisecond // probe every 100ms
	n := &fakeNotifier{}
	ctx, cancel := context.WithTimeout(context.Background(), 550*time.Millisecond)
	defer cancel()

	tick := time.NewTicker(probeEvery(interval))
	defer tick.Stop()
	pinger(ctx, n, func() bool { return true }, interval/2, tick.C, quietLog())

	// Four ticks fit in the window; assert a lower bound only, because a busy
	// test machine may drop some and a flaky test here is worse than a loose one.
	if got := n.count(); got < 3 {
		t.Errorf("%d keepalives in %v at a %v cadence, want at least 3", got, 550*time.Millisecond, probeEvery(interval))
	}
}

// Not under systemd — the ordinary manual run — must send nothing, log nothing
// alarming, and return immediately.
func TestRunWithoutAWatchdogDoesNothing(t *testing.T) {
	t.Setenv("WATCHDOG_USEC", "")
	t.Setenv("NOTIFY_SOCKET", "")

	probed := false
	done := make(chan struct{})
	go func() {
		defer close(done)
		Run(context.Background(), func() bool { probed = true; return true }, quietLog())
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Run blocked with no watchdog configured")
	}
	if probed {
		t.Error("probed for liveness with no watchdog configured")
	}
}
