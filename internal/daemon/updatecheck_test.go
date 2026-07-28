// SPDX-License-Identifier: MIT

package daemon

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/phil9922/backup-maker/internal/config"
	"github.com/phil9922/backup-maker/internal/update"
)

// fakeGitHub stands in for the releases endpoint and counts how often it is
// actually called — which is the point of most of these tests.
func fakeGitHub(t *testing.T, tag string, calls *atomic.Int32) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"tag_name":"` + tag + `"}`))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func updateDaemon(t *testing.T, cfg *config.Config, srv *httptest.Server, sink *countingNotifier) *daemon {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	t.Setenv("HOME", dir)
	t.Setenv("AppData", dir)
	t.Setenv("LOCALAPPDATA", dir)

	a := newAlerter(nil, slog.New(slog.DiscardHandler), true)
	if sink != nil {
		a.setNotifier(sink)
	}
	return &daemon{
		cfg:        cfg,
		state:      &config.State{},
		log:        slog.New(slog.DiscardHandler),
		alerts:     a,
		newChecker: func() update.Checker { return update.Checker{URL: srv.URL, Client: srv.Client()} },
	}
}

// waitForAlerts polls until n alerts have been delivered, or gives up.
//
// Alerts are FIRE-AND-FORGET on a goroutine, deliberately — nothing about a
// backup may wait on a notification daemon — so a test that reads the count
// straight after the call reads it before delivery has happened.
func waitForAlerts(t *testing.T, sink *countingNotifier, n int) {
	t.Helper()
	for range 200 {
		if sink.count() >= n {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("waited for %d alert(s), saw %d", n, sink.count())
}

// THE PROMISE THIS KEEPS. The README, docs/security.md and the dashboard all
// state that nothing reaches the internet unless the user asked for it. Update
// checking is the only thing in the program that would, so "off" must mean no
// request is made AT ALL — not a request whose answer is discarded.
func TestCheckingOffMakesNoRequestWhatsoever(t *testing.T) {
	var calls atomic.Int32
	cfg := config.New()
	cfg.General.UpdateCheck = false
	d := updateDaemon(t, cfg, fakeGitHub(t, "v99.0.0", &calls), nil)

	d.checkForUpdate(context.Background())

	if n := calls.Load(); n != 0 {
		t.Errorf("update checking is off and %d request(s) were still made to the internet", n)
	}
}

// ANNOUNCED ONCE PER VERSION. Checking runs daily; an alert that repeats every
// day until you upgrade is one you learn to dismiss without reading — and it
// shares a channel with "your backups have stopped".
func TestANewReleaseIsAnnouncedOnceNotEveryDay(t *testing.T) {
	var calls atomic.Int32
	var sink countingNotifier
	cfg := config.New()
	cfg.General.UpdateCheck = true
	d := updateDaemon(t, cfg, fakeGitHub(t, "v99.0.0", &calls), &sink)

	// A release version to compare against, since a "dev" build never checks.
	d.state.UpdateLastCheck = time.Time{}
	d.versionOverride = "0.1.6"

	d.checkForUpdate(context.Background())
	waitForAlerts(t, &sink, 1)

	// The next day comes round: the check runs again, the answer is the same.
	d.state.UpdateLastCheck = time.Now().Add(-48 * time.Hour)
	d.checkForUpdate(context.Background())
	// Give a second announcement time to arrive if the rule is broken.
	time.Sleep(200 * time.Millisecond)
	if sink.count() != 1 {
		t.Errorf("the same release was announced %d times — a daily nag", sink.count())
	}
	if calls.Load() != 2 {
		t.Errorf("expected 2 checks, got %d", calls.Load())
	}
}

// Being already up to date is silence, not a "you are current" notification.
func TestBeingUpToDateAnnouncesNothing(t *testing.T) {
	var calls atomic.Int32
	var sink countingNotifier
	cfg := config.New()
	cfg.General.UpdateCheck = true
	d := updateDaemon(t, cfg, fakeGitHub(t, "v0.1.6", &calls), &sink)
	d.versionOverride = "0.1.6"

	d.checkForUpdate(context.Background())
	time.Sleep(200 * time.Millisecond)

	if sink.count() != 0 {
		t.Errorf("being current produced %d alerts", sink.count())
	}
	if d.updateAvailable() != "" {
		t.Errorf("updateAvailable() = %q while current", d.updateAvailable())
	}
}

// A DAEMON IN A RESTART LOOP MUST NOT HAMMER A PUBLIC API. The last-checked
// time is persisted for exactly this reason: unauthenticated callers share a
// 60-per-hour budget per address, and a crash loop would burn it in a minute.
func TestACheckWithinTheDayIsSkipped(t *testing.T) {
	var calls atomic.Int32
	cfg := config.New()
	cfg.General.UpdateCheck = true
	d := updateDaemon(t, cfg, fakeGitHub(t, "v99.0.0", &calls), nil)
	d.versionOverride = "0.1.6"

	d.state.UpdateLastCheck = time.Now().Add(-1 * time.Hour)
	d.checkForUpdate(context.Background())

	if n := calls.Load(); n != 0 {
		t.Errorf("checked again after 1 hour (%d requests); the daily interval is not being honoured", n)
	}
}

// A failure must not look like a backup fault, and must not retry in a tight
// loop either — the attempt is recorded even though it failed.
func TestAFailedCheckIsRecordedSoItRetriesTomorrowNotImmediately(t *testing.T) {
	cfg := config.New()
	cfg.General.UpdateCheck = true
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(srv.Close)
	var sink countingNotifier
	d := updateDaemon(t, cfg, srv, &sink)
	d.versionOverride = "0.1.6"

	d.checkForUpdate(context.Background())
	time.Sleep(200 * time.Millisecond)

	if d.state.UpdateLastCheck.IsZero() {
		t.Error("a failed check was not recorded, so it would retry on every tick")
	}
	if sink.count() != 0 {
		t.Error("a failed check alerted the user about something")
	}
}

// updateAvailable is what the dashboard and `status` read. Switching checking
// off must also stop it REPORTING, not just stop it asking — otherwise a stale
// answer sits on screen after the user opted out.
func TestSwitchingCheckingOffAlsoStopsReporting(t *testing.T) {
	var calls atomic.Int32
	cfg := config.New()
	cfg.General.UpdateCheck = true
	d := updateDaemon(t, cfg, fakeGitHub(t, "v99.0.0", &calls), nil)
	d.versionOverride = "0.1.6"

	d.checkForUpdate(context.Background())
	if d.updateAvailable() != "v99.0.0" {
		t.Fatalf("updateAvailable() = %q, want v99.0.0", d.updateAvailable())
	}

	d.cfg.General.UpdateCheck = false
	if got := d.updateAvailable(); got != "" {
		t.Errorf("updateAvailable() = %q after opting out; a stale answer stays on screen", got)
	}
}
