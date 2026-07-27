// SPDX-License-Identifier: MIT

// Package watchdog tells the service manager that this process is not merely
// running but still able to make progress, so a daemon that has DEADLOCKED is
// restarted instead of sitting at "active (running)" for ever, backing nothing
// up and telling nobody.
//
// systemd already restarts a daemon that crashes. This closes the other half:
// a process that is alive, holding its lock, and going nowhere.
//
// THE LIVENESS SIGNAL IS THE CALLER'S BUSINESS, and the caller is expected to
// hand in a probe that CANNOT BLOCK and CANNOT TOUCH THE NETWORK — see
// internal/daemon/watchdog.go for the reasoning about which probe is safe. All
// this package does is turn a stream of yes/no answers into "keep feeding the
// watchdog" or "stop, and let systemd act".
//
// Nothing in this package is enabled unless the service manager asked for it:
// with no $WATCHDOG_USEC in the environment — running the daemon from a
// terminal, or a unit with no WatchdogSec= — Run returns immediately and
// nothing is ever sent.
package watchdog

import (
	"context"
	"log/slog"
	"time"
)

// Notifier delivers one keepalive to the service manager.
type Notifier interface {
	// Ping sends a single WATCHDOG=1. The error is for a debug log: a
	// keepalive that failed to send is systemd's problem to act on, not
	// something this process can fix.
	Ping() error
	Close() error
}

const (
	// slowestProbe is the ordinary cadence: ten seconds, which for the
	// WatchdogSec=180 the unit installs is eighteen pings per window. Probing
	// is one uncontended TryLock and one datagram, so the cost of being
	// generous here is nil, and the benefit is that a wedge is noticed in
	// seconds rather than at the end of a long window.
	slowestProbe = 10 * time.Second
	// fastestProbe stops a pathologically short WATCHDOG_USEC (a hand-written
	// unit with WatchdogSec=100ms) from turning this into a spin loop.
	fastestProbe = 100 * time.Millisecond
)

// Run feeds the service manager's watchdog for as long as alive answers, and
// blocks until ctx is done.
//
// It is a no-op — one env lookup and a return — when no watchdog is configured,
// which includes every non-Linux build and every run outside systemd.
func Run(ctx context.Context, alive func() bool, log *slog.Logger) {
	n, interval, err := open()
	if err != nil {
		// Something asked for a watchdog and it cannot be fed. Loud, because
		// the consequence is severe and silent otherwise: systemd will kill and
		// restart this process once per watchdog interval, for ever, and the
		// journal would show only the killing.
		log.Error("a systemd watchdog is configured but cannot be reached; this service may be restarted repeatedly", "err", err)
		return
	}
	if n == nil {
		return // not under a watchdog: the normal case for a manual run
	}
	defer n.Close()

	every := probeEvery(interval)
	log.Info("systemd watchdog active", "interval", interval, "probe_every", every,
		"gives_up_after", interval/2)
	tick := time.NewTicker(every)
	defer tick.Stop()
	pinger(ctx, n, alive, interval/2, tick.C, log)
}

// pinger is the loop, with its tick source supplied so the tests can drive it
// exactly. Each tick carries its own timestamp — that is time.Ticker's
// contract — so the loop needs no clock of its own and the tests need no sleeps.
//
// There is deliberately no ping before the first tick: probeEvery keeps the
// first one at a quarter of the watchdog interval or sooner, comfortably inside
// the window systemd is measuring.
func pinger(ctx context.Context, n Notifier, alive func() bool, grace time.Duration, ticks <-chan time.Time, log *slog.Logger) {
	t := tracker{grace: grace}
	healthy := true
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-ticks:
			ok := t.observe(alive(), now)
			if ok != healthy {
				healthy = ok
				if ok {
					log.Warn("the daemon lock is available again; telling systemd we are alive")
				} else {
					// The last thing in the journal before the kill, and the
					// only evidence of WHY it happened.
					log.Error("the daemon lock has been unobtainable for too long; this looks like a deadlock, so systemd is no longer being told this service is alive",
						"stuck_for", now.Sub(t.stuckSince), "grace", grace)
				}
			}
			if !ok {
				continue
			}
			if err := n.Ping(); err != nil {
				log.Debug("could not send a watchdog keepalive", "err", err)
			}
		}
	}
}

// tracker turns a stream of lock probes into the one decision that matters:
// does this process still look alive enough to keep the watchdog fed.
//
// A SINGLE FAILED PROBE MEANS NOTHING, and treating it as evidence is how a
// watchdog kills healthy daemons. The daemon lock is legitimately held for a
// stretch on every config apply, and a probe that lands inside one has learned
// nothing about liveness. Only unavailability that lasts PAST GRACE — half the
// watchdog interval, so systemd's own timer still has to run out on top of it —
// is evidence of a wedge.
type tracker struct {
	grace time.Duration
	// stuckSince is when the current unbroken run of failed probes began. Zero
	// while the last probe succeeded, which is what makes recovery reset the
	// clock rather than accumulate failures over the lifetime of the process.
	stuckSince time.Time
}

// observe records one probe result and reports whether to keep pinging.
func (t *tracker) observe(gotLock bool, now time.Time) bool {
	if gotLock {
		t.stuckSince = time.Time{}
		return true
	}
	if t.stuckSince.IsZero() {
		t.stuckSince = now
	}
	return now.Sub(t.stuckSince) <= t.grace
}

// probeEvery is the cadence for a given watchdog interval: often enough that
// several pings fit in every window (one lost datagram, or one probe that lands
// during a legitimate lock hold, must never be enough to miss a deadline),
// capped so the usual 180-second window does not mean 180 seconds of not
// looking.
func probeEvery(interval time.Duration) time.Duration {
	every := slowestProbe
	if quarter := interval / 4; quarter < every {
		every = quarter
	}
	if every < fastestProbe {
		every = fastestProbe
	}
	return every
}
