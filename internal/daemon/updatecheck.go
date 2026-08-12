// SPDX-License-Identifier: MIT

package daemon

import (
	"context"
	"time"

	"github.com/phil9922/backup-maker/internal/config"
	"github.com/phil9922/backup-maker/internal/update"
	"github.com/phil9922/backup-maker/internal/version"
)

const (
	// updateCheckEvery is how often github.com is asked. Once a day: releases
	// are not frequent enough to warrant more, and an unauthenticated caller
	// shares a 60-per-hour budget with every other machine behind the same
	// address.
	updateCheckEvery = 24 * time.Hour
	// updateFirstCheckAfter delays the first check past startup. A daemon
	// starting is usually a machine waking, rebooting or being upgraded, and
	// none of those is a moment to add a network round trip to — nor to
	// interrupt somebody who has just logged in.
	updateFirstCheckAfter = 5 * time.Minute
)

// updateLoop tells the user when a newer release exists, and does nothing else.
//
// NOTHING IS DOWNLOADED, REPLACED OR RUN. The ceiling is deliberate: an updater
// that installs by itself can stop backups on every machine at once, which is
// the exact failure this program exists to prevent. See package update.
//
// The loop runs always; the SETTING is checked at each tick rather than at
// startup, so switching it on in the dashboard takes effect without a restart —
// and switching it off stops the next check rather than the one after.
func (d *daemon) updateLoop(ctx context.Context) {
	timer := time.NewTimer(updateFirstCheckAfter)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}
		d.checkForUpdate(ctx)
		timer.Reset(updateCheckEvery)
	}
}

// checkForUpdate performs one check, if the user has asked for checks at all.
func (d *daemon) checkForUpdate(ctx context.Context) {
	if !d.currentCfg().General.UpdateCheck {
		return
	}
	// A build with no release version cannot be compared with one, so there is
	// nothing to ask about — and asking anyway would have every developer's
	// working tree talking to github.com.
	if !update.Comparable(d.runningVersion()) {
		return
	}

	d.mu.Lock()
	last := d.state.UpdateLastCheck
	d.mu.Unlock()
	// Persisted across restarts on purpose: a daemon in a restart loop would
	// otherwise ask on every start, which is how one machine exhausts a shared
	// rate limit.
	if !last.IsZero() && time.Since(last) < updateCheckEvery {
		return
	}

	// The request happens with the lock FREE. It is a network round trip to the
	// internet, and d.mu is the lock every status read takes — holding it
	// across a call to github.com would stall the dashboard behind a slow or
	// blackholed connection. Same rule the config-apply path is shaped around.
	latest, err := d.checker().Latest(ctx)
	if err != nil {
		// Debug, not warning. Being unable to reach github.com says nothing
		// about whether backups are working, and this is a convenience the user
		// switched on — it must never look like a fault with their backups.
		d.log.Debug("could not check for a newer release", "err", err)
		// The attempt is still recorded, so a machine that cannot reach
		// github.com retries tomorrow rather than on every tick.
		d.recordUpdateCheck(time.Now(), "", "")
		return
	}

	current := d.runningVersion()
	announce := update.Newer(current, latest)
	d.mu.Lock()
	already := d.state.UpdateAnnounced == latest
	d.mu.Unlock()
	// ANNOUNCED ONCE PER VERSION, not once a day until it is installed. A
	// notification that repeats is one the user learns to dismiss without
	// reading, and these share a channel with "your backups have stopped".
	announce = announce && !already

	seen := ""
	if announce {
		seen = latest
	}
	d.recordUpdateCheck(time.Now(), latest, seen)
	if !announce {
		return
	}
	d.log.Info("a newer release is available", "current", current, "latest", latest)
	d.alerts.updateAvailable(latest)
}

// runningVersion is this build's release version. A seam only because the real
// value is injected by the linker and is package-private to internal/version,
// so a test cannot otherwise put the daemon on a specific release — and every
// interesting case here ("is 0.1.7 newer than what I am running") needs one.
func (d *daemon) runningVersion() string {
	if d.versionOverride != "" {
		return d.versionOverride
	}
	return version.Get().Version
}

// checker is the release checker to use. nil means the real one — the same
// "nil is the real implementation" seam newBackend and machines.StorageFor use.
// It exists so a test can prove the two things that matter and cannot be shown
// against the live endpoint: that switching the setting OFF makes no request at
// all, and that an update is announced exactly once.
func (d *daemon) checker() update.Checker {
	if d.newChecker == nil {
		return update.Checker{}
	}
	return d.newChecker()
}

// recordUpdateCheck persists the outcome of one check under the lock the
// package documents as guarding d.state. announced is set only when the user
// was actually told, so the "announce once" rule survives a restart.
func (d *daemon) recordUpdateCheck(at time.Time, latest, announced string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if err := d.updateState(func(s *config.State) error {
		s.UpdateLastCheck = at
		if latest != "" {
			s.UpdateLatest = latest
		}
		if announced != "" {
			s.UpdateAnnounced = announced
		}
		return nil
	}); err != nil {
		d.log.Debug("could not record the update check", "err", err)
	}
}

// updateCheckedAt reports when a check last succeeded, so the panel can say
// "checked an hour ago" instead of implying a freshness it has not earned.
func (d *daemon) updateCheckedAt() time.Time {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.state.UpdateLastCheck
}

// updateComparable reports whether this build has a release version to compare
// at all. False for anything built from source, where no check is ever made.
func (d *daemon) updateComparable() bool {
	return update.Comparable(d.runningVersion())
}

// updateAvailable returns the newest release seen, or "" when there is nothing
// newer than what is running. Read by the dashboard and `backup-maker status`.
func (d *daemon) updateAvailable() string {
	if !d.currentCfg().General.UpdateCheck {
		return ""
	}
	d.mu.Lock()
	latest := d.state.UpdateLatest
	d.mu.Unlock()
	if !update.Newer(d.runningVersion(), latest) {
		return ""
	}
	return latest
}
