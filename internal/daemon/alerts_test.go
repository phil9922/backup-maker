// SPDX-License-Identifier: MIT

package daemon

import (
	"context"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/phil9922/backup-maker/internal/localmirror"
	"github.com/phil9922/backup-maker/internal/notify"
	"github.com/phil9922/backup-maker/internal/pairing"
	"github.com/phil9922/backup-maker/internal/status"
	"github.com/phil9922/backup-maker/internal/statuspage"
)

// recordingNotifier stands in for the desktop. err and block let a test make it
// behave like the ones that exist in the wild: absent, broken, or wedged.
type recordingNotifier struct {
	mu    sync.Mutex
	got   []alert
	err   error
	block chan struct{} // when non-nil, Notify waits for it (or for ctx)
}

func (n *recordingNotifier) Notify(ctx context.Context, u notify.Urgency, title, body string) error {
	if n.block != nil {
		select {
		case <-n.block:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	n.got = append(n.got, alert{urgency: u, title: title, body: body})
	return n.err
}

func (n *recordingNotifier) count() int {
	n.mu.Lock()
	defer n.mu.Unlock()
	return len(n.got)
}

// waitFor blocks until want notifications have been delivered, then makes sure
// no more turn up. Delivery is deliberately asynchronous, so both halves matter.
func (n *recordingNotifier) waitFor(t *testing.T, want int) []alert {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for n.count() < want && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	time.Sleep(50 * time.Millisecond) // a stray extra would land by now
	n.mu.Lock()
	defer n.mu.Unlock()
	if len(n.got) != want {
		t.Fatalf("delivered %d notifications, want %d: %+v", len(n.got), want, n.got)
	}
	return append([]alert(nil), n.got...)
}

// testAlerter is an alerter with the desktop replaced by something that can be
// asserted on.
func testAlerter(enabled bool) (*alerter, *recordingNotifier) {
	n := &recordingNotifier{}
	return newAlerter(n, slog.New(slog.NewTextHandler(io.Discard, nil)), enabled), n
}

// targetModel is a health model with one destination in the given state.
func targetModel(name, state string, lastSeen time.Time) status.Model {
	return status.Model{
		MachineName: "workstation",
		Targets:     []status.TargetInfo{{Name: name, Type: "share", State: state, LastSeen: lastSeen}},
	}
}

func titles(alerts []alert) string {
	var sb strings.Builder
	for _, a := range alerts {
		sb.WriteString(a.title)
		sb.WriteString(" | ")
	}
	return sb.String()
}

func onlyAlert(t *testing.T, alerts []alert) alert {
	t.Helper()
	if len(alerts) != 1 {
		t.Fatalf("expected exactly one alert, got %d: %s", len(alerts), titles(alerts))
	}
	return alerts[0]
}

// The whole point of the feature, and the whole point of doing it on
// transitions: a destination that stops working says so once, keeps quiet while
// it stays broken, and says so again — gently — when it comes back.
func TestStaleDestinationAlertsOnceAndRecoveryAlertsOnce(t *testing.T) {
	a, _ := testAlerter(true)
	now := time.Date(2026, 7, 26, 12, 0, 0, 0, time.UTC)
	seen := now.Add(-8 * 24 * time.Hour)

	if got := a.pending(targetModel("nas", "in sync", now), now); len(got) != 0 {
		t.Fatalf("a healthy destination alerted: %s", titles(got))
	}

	broke := onlyAlert(t, a.pending(targetModel("nas", "stale", seen), now))
	if broke.urgency != notify.Critical {
		t.Errorf("a destination that stopped backing up was not critical: %+v", broke)
	}
	if !strings.Contains(broke.title, "nas") {
		t.Errorf("the alert does not name the destination: %+v", broke)
	}
	if !strings.Contains(broke.body, "8 days ago") {
		t.Errorf("the alert does not say how long it has been: %+v", broke)
	}

	// Still stale, cycle after cycle. Repeating this every minute is how a user
	// learns to ignore backup-maker entirely.
	for i := range 3 {
		if got := a.pending(targetModel("nas", "stale", seen), now.Add(time.Duration(i)*time.Minute)); len(got) != 0 {
			t.Fatalf("a destination that was already broken alerted again: %s", titles(got))
		}
	}

	back := onlyAlert(t, a.pending(targetModel("nas", "syncing", now), now))
	if back.urgency != notify.Normal {
		t.Errorf("good news interrupted the user: %+v", back)
	}
	if !strings.Contains(back.title, "nas") || !strings.Contains(back.title, "again") {
		t.Errorf("the recovery alert does not say what recovered: %+v", back)
	}
	if got := a.pending(targetModel("nas", "in sync", now), now); len(got) != 0 {
		t.Fatalf("a destination that is simply fine alerted: %s", titles(got))
	}
}

// A laptop's card is unplugged several times a day and a NAS sleeps every
// night. Nagging about that is exactly how the one alert that matters gets
// trained away.
func TestBrieflyOfflineDestinationNeverAlerts(t *testing.T) {
	a, _ := testAlerter(true)
	now := time.Date(2026, 7, 26, 12, 0, 0, 0, time.UTC)

	for _, state := range []string{"in sync", "offline", "offline", "syncing", "in sync", "awaiting-pair", "offline"} {
		if got := a.pending(targetModel("sdcard", state, now), now); len(got) != 0 {
			t.Fatalf("state %q alerted: %s", state, titles(got))
		}
	}
}

// A full destination is not backing anything up, and reclaiming has already
// failed to find anything it is allowed to delete.
func TestFullDestinationAlertsCriticallyOnce(t *testing.T) {
	a, _ := testAlerter(true)
	now := time.Date(2026, 7, 26, 12, 0, 0, 0, time.UTC)

	a.pending(targetModel("sdcard", "in sync", now), now)
	full := onlyAlert(t, a.pending(targetModel("sdcard", "full", now), now))
	if full.urgency != notify.Critical {
		t.Errorf("a full destination was not critical: %+v", full)
	}
	if !strings.Contains(full.title, "sdcard") || !strings.Contains(full.title, "full") {
		t.Errorf("the alert does not say what is wrong: %+v", full)
	}

	// Unplugged while full, then plugged back in: the user is not told it broke
	// a second time, and IS told when it finally works again.
	if got := a.pending(targetModel("sdcard", "offline", now), now); len(got) != 0 {
		t.Fatalf("unplugging a full card alerted: %s", titles(got))
	}
	if got := a.pending(targetModel("sdcard", "full", now), now); len(got) != 0 {
		t.Fatalf("the same full card was announced twice: %s", titles(got))
	}
	back := onlyAlert(t, a.pending(targetModel("sdcard", "in sync", now), now))
	if back.urgency != notify.Normal {
		t.Errorf("recovery from full was not normal urgency: %+v", back)
	}
}

// A scheduled snapshot that fails is a backup that did not happen — and,
// because that alert is sticky, a later run that works has to withdraw it.
func TestFailedSnapshotAlertsCriticallyOnceAndItsAllClearOnce(t *testing.T) {
	a, _ := testAlerter(true)
	now := time.Date(2026, 7, 26, 12, 0, 0, 0, time.UTC)

	job := func(state, detail string) status.Model {
		return status.Model{Archives: []status.ArchiveRow{{
			Name: "weekly-code", Target: "nas", State: state, Detail: detail,
		}}}
	}

	// A job that has only ever worked has nothing to announce, in either
	// direction.
	if got := a.pending(job("ok", ""), now); len(got) != 0 {
		t.Fatalf("a healthy snapshot job alerted: %s", titles(got))
	}

	got := onlyAlert(t, a.pending(job("failed", "no space left on device"), now))
	if got.urgency != notify.Critical {
		t.Errorf("a failed snapshot was not critical: %+v", got)
	}
	if !strings.Contains(got.title, "weekly-code") {
		t.Errorf("the alert does not name the job: %+v", got)
	}
	if !strings.Contains(got.body, "no space left on device") {
		t.Errorf("the alert drops the reason it failed: %+v", got)
	}
	if again := a.pending(job("failed", "no space left on device"), now); len(again) != 0 {
		t.Fatalf("a job that is still failed alerted again: %s", titles(again))
	}

	// Waiting for its next run is not success, and must not withdraw anything.
	if got := a.pending(job("due", ""), now); len(got) != 0 {
		t.Fatalf("a job that is merely due was reported as recovered: %s", titles(got))
	}

	clear := onlyAlert(t, a.pending(job("ok", ""), now))
	if clear.urgency != notify.Normal {
		t.Errorf("the snapshot all-clear interrupted the user: %+v", clear)
	}
	if !strings.Contains(clear.title, "weekly-code") {
		t.Errorf("the all-clear does not name the job: %+v", clear)
	}
	if again := a.pending(job("ok", ""), now); len(again) != 0 {
		t.Fatalf("the all-clear repeated: %s", titles(again))
	}
}

// Fail, fix, fail: each turn is news exactly once, and the sticky alert is
// withdrawn in between rather than accumulating.
func TestSnapshotFailingAgainAfterRecoveryAlertsAgain(t *testing.T) {
	a, _ := testAlerter(true)
	now := time.Date(2026, 7, 26, 12, 0, 0, 0, time.UTC)
	job := func(state string) status.Model {
		return status.Model{Archives: []status.ArchiveRow{{Name: "weekly-code", Target: "nas", State: state}}}
	}

	var got []alert
	for _, state := range []string{"ok", "failed", "failed", "ok", "ok", "failed"} {
		got = append(got, a.pending(job(state), now)...)
	}
	if len(got) != 3 {
		t.Fatalf("fail/fix/fail raised %d alerts, want 3: %s", len(got), titles(got))
	}
	want := []notify.Urgency{notify.Critical, notify.Normal, notify.Critical}
	for i, u := range want {
		if got[i].urgency != u {
			t.Errorf("alert %d: urgency %v, want %v (%s)", i, got[i].urgency, u, got[i].title)
		}
	}
}

// Pairing is the one case where the user must act for anything to happen at
// all — but it is good news, not a failure, so it fades.
func TestPairRequestAlertsNormallyAndNeverReplaysWhatWasAlreadyWaiting(t *testing.T) {
	a, _ := testAlerter(true)
	now := time.Date(2026, 7, 26, 12, 0, 0, 0, time.UTC)
	waiting := status.Model{PendingSources: []pairing.PendingSource{{DeviceID: "AAAA-BBBB", Name: "their-laptop"}}}

	// Already waiting when the daemon started: recorded, not announced. A
	// restart must not re-pop something that has been sitting on the dashboard.
	if got := a.pending(waiting, now); len(got) != 0 {
		t.Fatalf("a pending request that predates us was replayed: %s", titles(got))
	}
	if got := a.pending(waiting, now); len(got) != 0 {
		t.Fatalf("a pending request alerted on a later cycle: %s", titles(got))
	}

	both := status.Model{PendingSources: []pairing.PendingSource{
		{DeviceID: "AAAA-BBBB", Name: "their-laptop"},
		{DeviceID: "CCCC-DDDD", Name: "the-pi"},
	}}
	got := onlyAlert(t, a.pending(both, now))
	if got.urgency != notify.Normal {
		t.Errorf("a pairing request interrupted the user: %+v", got)
	}
	if !strings.Contains(got.title, "the-pi") {
		t.Errorf("the alert does not say which machine: %+v", got)
	}
}

// Starting up must not mean staying silent about a problem that is already
// there — and must not mean replaying history either.
func TestFirstCycleAlertsForProblemsThatWereAlreadyThere(t *testing.T) {
	a, _ := testAlerter(true)
	now := time.Date(2026, 7, 26, 12, 0, 0, 0, time.UTC)
	m := status.Model{
		Targets: []status.TargetInfo{
			{Name: "nas", State: "stale", LastSeen: now.Add(-30 * 24 * time.Hour)},
			{Name: "sdcard", State: "in sync", LastSeen: now},
		},
		Archives:       []status.ArchiveRow{{Name: "weekly-code", Target: "sdcard", State: "failed"}},
		PendingSources: []pairing.PendingSource{{DeviceID: "AAAA-BBBB", Name: "their-laptop"}},
	}

	got := a.pending(m, now)
	if len(got) != 2 {
		t.Fatalf("the first cycle raised %d alerts, want the stale destination and the failed snapshot only: %s",
			len(got), titles(got))
	}
	for _, al := range got {
		if al.urgency != notify.Critical {
			t.Errorf("a first-cycle alert was not about something broken: %+v", al)
		}
	}
	if again := a.pending(m, now.Add(time.Minute)); len(again) != 0 {
		t.Fatalf("the same pre-existing problems alerted a second time: %s", titles(again))
	}
}

// Off means off: not quieter, not deferred.
func TestDesktopAlertsOffSendsNothing(t *testing.T) {
	a, n := testAlerter(false)
	now := time.Date(2026, 7, 26, 12, 0, 0, 0, time.UTC)

	a.check(targetModel("nas", "stale", now.Add(-8*24*time.Hour)), now)
	a.foreignStorage("nas", "//nas/backups")
	if got := n.count(); got != 0 {
		t.Fatalf("%d notifications were sent with desktop_alerts off", got)
	}

	// Switching them on compares against nothing, so the problem that is
	// already there is announced rather than being missed for ever.
	a.setEnabled(true)
	if got := a.pending(targetModel("nas", "stale", now.Add(-8*24*time.Hour)), now); len(got) != 1 {
		t.Fatalf("turning alerts on raised %d alerts about an already-broken destination, want 1: %s",
			len(got), titles(got))
	}
}

// A desktop that never answers is the Raspberry Pi case with a session bus and
// no notification service. It must cost the status loop nothing at all.
func TestAWedgedNotifierNeverBlocksOrDelaysTheStatusPage(t *testing.T) {
	root := t.TempDir()
	if err := localmirror.WriteMarkerAt(root, "our-uuid", "workstation"); err != nil {
		t.Fatal(err)
	}
	w := newDestWriter(t, root, "our-uuid")
	blocked := &recordingNotifier{block: make(chan struct{})}
	defer close(blocked.block)
	w.d.alerts = newAlerter(blocked, w.d.log, true)

	now := time.Date(2026, 7, 26, 12, 0, 0, 0, time.UTC)
	collect := func() status.Model { return targetModel("card", "stale", now.Add(-8*24*time.Hour)) }

	done := make(chan struct{})
	go func() {
		defer close(done)
		w.d.cycle(collect)
		w.d.cycle(collect)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the status loop waited for the desktop")
	}

	if !exists(t, root, statuspage.PathFor("workstation")) {
		t.Error("a wedged notifier cost the destination its status page")
	}
	if blocked.count() != 0 {
		t.Fatal("the notifier was supposed to be stuck; the test proves nothing")
	}
}

// A notifier that simply fails — no binary, no notification daemon — is the
// normal headless case, not an error anybody should hear about.
func TestAFailingNotifierIsSwallowed(t *testing.T) {
	a, n := testAlerter(true)
	n.err = context.DeadlineExceeded
	now := time.Date(2026, 7, 26, 12, 0, 0, 0, time.UTC)

	a.check(targetModel("nas", "stale", now.Add(-8*24*time.Hour)), now)
	got := n.waitFor(t, 1)
	if got[0].urgency != notify.Critical {
		t.Errorf("wrong urgency reached the desktop: %+v", got[0])
	}
	// And the transition is still remembered, so the failure doesn't turn into
	// a retry every minute.
	a.check(targetModel("nas", "stale", now.Add(-8*24*time.Hour)), now)
	n.waitFor(t, 1)
}

// Unrecognised storage is invisible in the health model — the destination reads
// as merely offline — so this is the only way the user is told that a card they
// think is backing up is being refused.
//
// THE BUG THIS ALSO GUARDS: that alert is sticky, and it had no all-clear.
// Plugging the right drive back in resumed the backups silently, leaving a
// notification on screen saying they had stopped. The user had to go and check
// by hand, which is the errand alerting is supposed to remove.
func TestForeignStorageAlertsCriticallyThenClearsWhenTheRealStorageReturns(t *testing.T) {
	root := t.TempDir()
	if err := localmirror.WriteMarkerAt(root, "somebody-elses-uuid", "their-laptop"); err != nil {
		t.Fatal(err)
	}
	w := newDestWriter(t, root, "our-uuid")
	n := &recordingNotifier{}
	w.d.alerts = newAlerter(n, w.d.log, true)

	w.pass()
	w.pass()
	got := n.waitFor(t, 1)
	if got[0].urgency != notify.Critical {
		t.Errorf("unrecognised storage was not critical: %+v", got[0])
	}
	if !strings.Contains(got[0].title, "card") {
		t.Errorf("the alert does not name the destination: %+v", got[0])
	}
	if !strings.Contains(got[0].body, root) {
		t.Errorf("the alert does not say where it is looking: %+v", got[0])
	}

	// The real card is plugged back in: the same marker, the case that actually
	// happens. Backups resume, and the user is told so — once.
	if err := localmirror.WriteMarkerAt(root, "our-uuid", "workstation"); err != nil {
		t.Fatal(err)
	}
	w.pass()
	w.pass()
	got = n.waitFor(t, 2)
	if got[1].urgency != notify.Normal {
		t.Errorf("the all-clear interrupted the user: %+v", got[1])
	}
	if !strings.Contains(got[1].title, "card") || !strings.Contains(got[1].title, "again") {
		t.Errorf("the all-clear does not say what came back: %+v", got[1])
	}
	if !strings.Contains(got[1].body, "resumed") {
		t.Errorf("the all-clear does not close the loop: %+v", got[1])
	}

	// And replacing it again is news again: alert, all-clear, alert.
	if err := localmirror.WriteMarkerAt(root, "somebody-elses-uuid", "their-laptop"); err != nil {
		t.Fatal(err)
	}
	w.pass()
	got = n.waitFor(t, 3)
	if got[2].urgency != notify.Critical {
		t.Errorf("a card going foreign a second time was not critical: %+v", got[2])
	}
}

// Nothing was ever wrong, so there is nothing to withdraw: a destination that
// has always been itself must not produce an all-clear on its first pass.
func TestHealthyDestinationRaisesNoAllClear(t *testing.T) {
	root := t.TempDir()
	if err := localmirror.WriteMarkerAt(root, "our-uuid", "workstation"); err != nil {
		t.Fatal(err)
	}
	w := newDestWriter(t, root, "our-uuid")
	n := &recordingNotifier{}
	w.d.alerts = newAlerter(n, w.d.log, true)

	w.pass()
	w.pass()
	time.Sleep(50 * time.Millisecond)
	if got := n.count(); got != 0 {
		t.Fatalf("a destination that was never foreign raised %d notifications", got)
	}
}

// A drive that is simply not plugged in is ordinary. It gets no notification,
// the same way it gets no log line.
func TestUnpluggedDestinationRaisesNoNotification(t *testing.T) {
	w := newDestWriter(t, t.TempDir()+"/not-mounted", "our-uuid")
	n := &recordingNotifier{}
	w.d.alerts = newAlerter(n, w.d.log, true)

	w.pass()
	w.pass()
	time.Sleep(50 * time.Millisecond)
	if got := n.count(); got != 0 {
		t.Fatalf("an absent drive raised %d notifications", got)
	}
}
