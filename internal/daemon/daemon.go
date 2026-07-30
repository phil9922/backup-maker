// SPDX-License-Identifier: MIT

// Package daemon composes the long-lived backup-maker process: single-instance
// lock, syncthing supervisor, local-mirror engines, and the web UI/API server.
package daemon

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/phil9922/backup-maker/internal/archive"
	"github.com/phil9922/backup-maker/internal/autostart"
	"github.com/phil9922/backup-maker/internal/config"
	"github.com/phil9922/backup-maker/internal/discover"
	"github.com/phil9922/backup-maker/internal/lanaddr"
	"github.com/phil9922/backup-maker/internal/localmirror"
	"github.com/phil9922/backup-maker/internal/notify"
	"github.com/phil9922/backup-maker/internal/pairing"
	"github.com/phil9922/backup-maker/internal/setup"
	"github.com/phil9922/backup-maker/internal/smbfs"
	"github.com/phil9922/backup-maker/internal/status"
	"github.com/phil9922/backup-maker/internal/syncthing"
	"github.com/phil9922/backup-maker/internal/update"
	"github.com/phil9922/backup-maker/internal/watchdog"
	"github.com/phil9922/backup-maker/internal/webui"
	"github.com/phil9922/backup-maker/internal/wol"
)

type daemon struct {
	log   *slog.Logger
	state *config.State

	// engineMu guards sup. The syncthing engine is LAZY: it is only
	// downloaded and started when a paired-machine target or receive mode is
	// configured. Drive/share-only setups never touch the network.
	engineMu sync.Mutex
	sup      *syncthing.Supervisor

	// waker broadcasts Wake-on-LAN packets to offline targets that have a
	// MAC configured. Safe for concurrent use; rate-limits per target.
	waker *wol.Waker

	// archiveRunning holds the schedules executing right now, keyed by job
	// name. Guarded by mu. A snapshot's last-run time is only written when it
	// completes, so this is the only way to tell a job writing a
	// multi-gigabyte zip from one that has never started.
	archiveRunning map[string]time.Time

	// archiveProgress is how far each running snapshot has got, keyed by job
	// name and guarded by mu. Memory only, and removed when the run ends: it
	// describes work in flight, and a figure left behind by a finished job
	// would be a bar stopped wherever it happened to reach.
	archiveProgress map[string]archive.Progress

	// tally is the lifetime "how much have you backed up for me" odometer.
	// Written by every mirror engine and by the snapshot writer, persisted in
	// batches rather than per file. Safe for concurrent use.
	tally *tally

	// marks remembers when each folder last synced to each drive/share
	// destination, so a restart does not reset those clocks to "never" and hide
	// a drive that has been missing for months. Persisted on the tally's flush.
	// Safe for concurrent use.
	marks *syncMarks

	// alerts pops a desktop notification when the health model crosses into (or
	// back out of) a state the user needs to know about. nil is a valid
	// alerter: every call on it is a no-op, which is what the tests that build
	// a bare daemon rely on.
	alerts *alerter

	mu      sync.Mutex
	cfg     *config.Config
	engines []*localmirror.Engine
	// reclaimers is one per destination name, so the dashboard can report what
	// was deleted to make room.
	reclaimers map[string]*localmirror.Reclaimer
	// statusPageBackends is one connection per drive/share destination, held
	// open so the status page can be refreshed without redialling SMB every
	// minute.
	statusPageBackends []namedBackend
	cancel             context.CancelFunc // stops the current engine set
	archiveResults     map[string]archive.Result

	// space caches the last-known-good free/total per destination, sampled off
	// the statusPageBackends connections once a minute. A destination that
	// goes offline keeps its last entry (marked stale by its timestamp) rather
	// than losing the reading entirely.
	spaceMu sync.Mutex
	space   map[string]spaceSample

	// descriptions is what each destination's marker file says it physically is,
	// keyed by target name and guarded by mu. Read when a destination is opened
	// (see prepareTargets), not on the status cycle: it changes when somebody
	// types a sentence, and asking the storage once a minute would be a round
	// trip for an answer that is almost never different.
	descriptions map[string]string

	// written remembers what was last put on each destination, so a page whose
	// content has not changed is not rewritten just because a minute has passed.
	// Only ever touched from the status-page goroutine.
	written map[string]lastWritten

	// foreign names the destinations whose storage we do not recognize, so the
	// refusal to write there is reported on the transition rather than once a
	// minute for ever. It has its own lock because the status writer asks the
	// same question while holding d.mu.
	foreignMu sync.Mutex
	foreign   map[string]bool
	// clashing names the destinations where another computer holds the machine
	// directory this one wants. Reported on the transition for the same reason
	// as foreign, and under the same lock, because a destination can only be in
	// one of the two states and the messages must not interleave.
	clashing map[string]bool

	// delivery remembers how each alert delivery method last performed.
	delivery *deliveryLog

	// lanMu guards the read-only network view's listener, which is started and
	// stopped at runtime now that the setting is a dashboard toggle rather than
	// a startup-only config key. Its own lock: switching it binds a socket, and
	// nothing that binds a socket may hold d.mu (see applyConfig).
	lanMu   sync.Mutex
	srv     *webui.Server
	lanView *webui.LANView
	// lanErr is why the view is not listening despite being switched on —
	// a port already taken, no LAN address. Carried to the dashboard rather
	// than left in the log, because a panel that invents its own explanation
	// for a switch that did not take is worse than one that says nothing.
	lanErr string

	// newBackend opens a destination, and exists only so tests can substitute
	// one that blocks. nil means the real one — the same "nil is the real
	// implementation" seam machines.StorageFor uses. A destination that hangs
	// is the entire hazard the config-apply path is shaped around, and it
	// cannot be demonstrated with storage that answers immediately.
	newBackend func(config.Target, map[string]string) (localmirror.Backend, time.Duration, bool, error)

	// docsID and docsBuild are the manual this build carries — its fingerprint,
	// and the files themselves. nil means the real documentation, the same "nil
	// is the real implementation" seam newBackend uses. They exist because the
	// embedded docs are installed by the main package, so a test binary carries
	// none at all and could otherwise not reach any of this.
	docsID    func() (string, error)
	docsBuild func() ([]webui.DocsFile, error)

	// startEngine starts the sync engine. nil means the real one — the same "nil
	// is the real implementation" seam newBackend and newChecker use.
	//
	// It exists because ensureEngine DOWNLOADS a pinned binary and runs it as a
	// child process, and a test asserting that a receive-only machine is recorded
	// as set up needs neither: it was reaching the network on every run, and on
	// Windows the running executable could not be deleted, so the temp directory
	// cleanup failed and reported it as the test failing.
	startEngine func(context.Context) error

	// newChecker builds the release checker, and exists only so tests can point
	// it at a recorder instead of github.com. nil means the real one.
	newChecker func() update.Checker
	// versionOverride replaces this build's release version. Tests only; empty
	// means the real one from internal/version.
	versionOverride string

	// retireMu serialises the actions on stopped folders. Each is a
	// read-modify-write cycle over config.toml, and one of them deletes
	// backups: two interleaved would lose a record or delete against a stale
	// one.
	retireMu sync.Mutex
}

// shareCredentials copies the stored share passwords out from under the lock,
// so that opening a destination — which dials — never has to hold it.
func (d *daemon) shareCredentials() map[string]string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return maps.Clone(d.state.ShareCredentials)
}

// spaceSample is one destination's last usage reading.
type spaceSample struct {
	free, total uint64
	at          time.Time
	// failed marks a destination that has NEVER managed to report its free
	// space — some NAS firmware simply never answers the query. Recorded
	// separately from "no entry yet" because the consequence is real: the
	// reclaim reserve cannot be enforced where free space can't be read, and a
	// blank card is exactly what a healthy paired machine looks like.
	failed bool
}

// needsEngine reports whether the config requires the machine-sync engine.
func needsEngine(cfg *config.Config) bool {
	if cfg.Receive.Enabled {
		return true
	}
	for _, t := range cfg.Targets {
		if t.Type == "device" {
			return true
		}
	}
	return false
}

// ensureEngine starts the syncthing engine if it isn't running yet. This is
// the ONLY path that triggers the (pinned, SHA256-verified) engine download.
func (d *daemon) ensureEngine(ctx context.Context) error {
	d.engineMu.Lock()
	defer d.engineMu.Unlock()
	if d.sup != nil {
		return nil
	}
	d.log.Info("machine sync configured; preparing sync engine")
	sup, err := syncthing.NewSupervisor(d.state, d.log)
	if err != nil {
		return fmt.Errorf("preparing sync engine: %w", err)
	}
	go sup.Run(ctx)
	if err := sup.WaitReady(ctx, 60*time.Second); err != nil {
		return err
	}
	if id, err := sup.Client.MyID(); err == nil {
		d.log.Info("sync engine ready", "device_id", id)
	}
	d.sup = sup
	go d.eventLoop(ctx)
	return nil
}

// engineUp starts the sync engine through whatever implementation is in force.
func (d *daemon) engineUp(ctx context.Context) error {
	if d.startEngine != nil {
		return d.startEngine(ctx)
	}
	return d.ensureEngine(ctx)
}

// engineClient returns the engine's REST client, or nil while the engine
// isn't running (no machine targets configured).
func (d *daemon) engineClient() *syncthing.Client {
	d.engineMu.Lock()
	defer d.engineMu.Unlock()
	if d.sup == nil {
		return nil
	}
	return d.sup.Client
}

// Run starts the daemon and blocks until ctx is cancelled or a fatal error
// occurs.
func Run(ctx context.Context, cfg *config.Config, log *slog.Logger) error {
	release, err := acquireLock()
	if err != nil {
		return err
	}
	defer release()

	// Set before the first Save below, so that a keyring this daemon cannot write
	// to is reported from the very first flush rather than from whenever the user
	// next happens to run a command. The secret is kept either way — see
	// State.storedSecrets — and this is the difference between a documented
	// fallback and a silent one.
	config.KeyringWriteFallback = func(account string, err error) {
		log.Warn("could not put a password in the OS keyring; it stays in state.json (mode 0600) instead, as it did before keyring storage was switched on",
			"entry", account, "err", err, "check", "backup-maker keychain status")
	}

	state, err := config.LoadState()
	if err != nil {
		return fmt.Errorf("loading state: %w", err)
	}
	// A KEYRING THAT WOULD NOT OPEN IS SAID OUT LOUD, ONCE, AT STARTUP. This is
	// the case the whole opt-in design is shaped around: a daemon started at boot
	// has no keyring session on most Linux desktops, and a locked keyring looks
	// identical. The secrets are not lost — they are in the keyring, and the keys
	// are still in state.json — but every destination and snapshot named here will
	// fail until it can be read, and it must never fail as "wrong password" with
	// no explanation anywhere. Beyond this line the ordinary per-destination
	// failure surfacing does the rest: there is deliberately no new alert
	// pipeline, because a machine in this state is not a machine whose backups
	// have silently stopped, it is one that is about to say so per destination.
	if misses := state.KeyringMisses(); len(misses) > 0 {
		log.Warn("the OS keyring would not give up stored passwords, so the destinations and snapshots listed cannot be used until it can be read; it is usually locked, or this daemon was started at boot with no keyring session",
			"entries", strings.Join(misses, ", "),
			"check", "backup-maker keychain status",
			"undo", "backup-maker keychain disable")
	}
	if state.IPCToken == "" {
		state.IPCToken = config.NewToken()
	}
	if state.InstallID == "" {
		// Minted here as well as in EnsureInstallID so an install that has only
		// ever been driven from the browser has one before the first destination
		// is written to. Same never-rotate rule as the token above.
		state.InstallID = config.NewToken()[:16]
	}
	state.DashboardPort = cfg.General.DashboardPort
	if err := state.Save(); err != nil {
		return fmt.Errorf("saving state: %w", err)
	}

	d := &daemon{
		log:   log,
		state: state,
		cfg:   cfg,
		waker: wol.NewWaker(wol.DefaultMinInterval, log),
	}
	// Built before applyConfig, which is the first thing that can find
	// unrecognised storage at a destination.
	d.delivery = newDeliveryLog()
	d.alerts = newAlerter(notify.Desktop(), log, cfg.General.DesktopAlerts)
	d.alerts.delivered = d.delivery.record
	d.alerts.recorded = d.recordAlert
	// Both built before applyConfig, which hands them to the engines it starts.
	// The marks come first: the tally's flush is what writes them out.
	d.marks = newSyncMarks(state)
	d.tally = newTally(state, d.saveState)
	go d.tally.run(ctx)
	// Covers the exit paths ctx cancellation doesn't reach (a dead listener),
	// so a shutdown keeps what it counted in the last few seconds.
	defer d.tally.flush()

	// Liveness reporting to systemd, so a daemon that deadlocks is restarted
	// rather than left "active (running)" backing nothing up. No-op when
	// there's no watchdog (a manual run, or an older unit file).
	//
	// STARTED BEFORE THE ENGINE, deliberately: the first run with a machine
	// target downloads the sync engine, which on a slow link can take longer
	// than the watchdog interval. The lock is free throughout that, so pings
	// continue and the download is never mistaken for a wedge.
	go watchdog.Run(ctx, d.lockResponsive, log)

	// ...and the case where that watchdog is not actually switched on. It lives
	// in the service definition, which only `autostart enable` rewrites, so an
	// upgraded binary can be running under an older machine's rules with every
	// protection above inert. Said here as well as in `status` because a
	// headless machine — the Pi — has nobody to run `status` on it. Advisory
	// only: rewriting a user's unit file from inside the daemon would be a
	// worse surprise than the problem.
	if r, err := autostart.Check(); err == nil && r.NeedsReinstall() {
		log.Warn("the installed service definition is out of date; restart policy and the wedged-daemon watchdog are not in effect until it is rewritten",
			"fix", "backup-maker autostart enable", "file", r.Path)
	}

	// Engine only when the config demands it; failure is fatal at startup
	// (matching previous behavior) but tolerated on later config reloads.
	if needsEngine(cfg) {
		if err := d.engineUp(ctx); err != nil {
			return err
		}
	} else {
		log.Info("no machine targets configured; sync engine stays off (nothing downloaded)")
	}

	d.applyConfig(ctx, cfg)
	go d.watchConfigFile(ctx)
	go d.archiveLoop(ctx)
	// Tells the user when a newer release exists, and does nothing else. The
	// loop always runs; the setting is read at each tick, and is OFF by
	// default — see config.General.UpdateCheck.
	go d.updateLoop(ctx)

	collector := &status.Collector{
		Cfg:                d.currentCfg,
		Client:             d.engineClient,
		Engines:            d.currentEngines,
		Archives:           d.archiveStatus,
		ArchiveRunning:     d.archiveRunningSnapshot,
		ArchiveProgress:    d.archiveProgressSnapshot,
		HasArchivePassword: d.hasArchivePassword,
		SetupDone:          d.setupDone,
		Space:              d.spaceSamples,
		Descriptions:       d.targetDescriptions,
		Totals:             d.totals,
		LANViewURL:         d.lanViewURL,
		LANViewProblem:     d.lanViewProblem,
		LANDevices:         d.lanDevices,
		WebhookURLSet:      d.webhookURLSet,
		NtfyTopicSet:       d.ntfyTopicSet,
		NtfyTokenSet:       d.ntfyTokenSet,
		NtfyTopicDisplay:   d.ntfyTopicDisplay,
		WebhookURLDisplay:  d.webhookURLDisplay,
		UpdateAvailable:    d.updateAvailable,
		UpdateCheckedAt:    d.updateCheckedAt,
		UpdateComparable:   d.updateComparable,
		Delivery:           d.delivery.snapshot,
		RecentAlerts:       d.alertHistory,
		Refused:            d.refusedTargets,
	}
	// Wake-on-LAN for offline targets that opted in with a MAC address.
	go d.wakeLoop(ctx, collector.Collect)
	// A status page on each destination, so backups can still be checked when
	// this machine is off.
	go d.statusPageLoop(ctx, collector.Collect)

	actions := buildActions(d)
	srv, err := webui.New(cfg, state, log, func() any { return collector.Collect() }, actions)
	if err != nil {
		return err
	}
	log.Info("dashboard listening", "url", fmt.Sprintf("http://127.0.0.1:%d", cfg.General.DashboardPort))

	// Optional read-only view for other devices on the network. Opt-in, and
	// never able to change anything: setup stays loopback-only.
	d.lanMu.Lock()
	d.srv = srv
	d.lanMu.Unlock()
	d.applyLANView(cfg)
	defer d.stopLANView()

	errCh := make(chan error, 1)
	go func() { errCh <- srv.Serve() }()

	select {
	case <-ctx.Done():
		srv.Shutdown()
		<-errCh
		return nil
	case err := <-errCh:
		return err
	}
}

func (d *daemon) currentCfg() *config.Config {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.cfg
}

func (d *daemon) currentEngines() []*localmirror.Engine {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]*localmirror.Engine(nil), d.engines...)
}

// applyConfig reconciles syncthing (starting it lazily if machine sync was
// just configured) and (re)starts local-mirror engines for the given config.
//
// THE CENTRAL LOCK IS HELD ONLY FOR THE SWAP AT THE END. Everything that has to
// touch a destination — opening it, and refreshing its adoption manifest —
// happens first, with d.mu free, because that work is network I/O to storage
// that may be switched off: smbfs allows 5 seconds to dial and 60 per
// operation, for each share separately. The watchdog reads liveness as "can
// d.mu still be taken" (see watchdog.go), so holding it across a handful of
// hanging shares would have systemd kill a daemon whose only problem is an
// unplugged NAS — precisely the reboot-loop-caused-by-a-missing-drive that the
// watchdog is written to avoid. Preparing first costs nothing in the healthy
// case and keeps the lock free in the unhealthy one.
//
// Doing it in this order is safe because applyConfig is never concurrent with
// itself: it is called once at startup and thereafter only from the single
// config-watcher goroutine.
func (d *daemon) applyConfig(ctx context.Context, cfg *config.Config) {
	// Before anything below can find unrecognised storage and want to say so.
	d.alerts.setEnabled(anyDeliveryOn(cfg))
	d.alerts.setKinds(cfg.General.Alerts)
	d.alerts.setNotifier(d.deliverySinks(cfg))
	if needsEngine(cfg) {
		if err := d.engineUp(ctx); err != nil {
			d.log.Error("sync engine unavailable; machine targets/receiving paused", "err", err)
		}
	}
	if c := d.engineClient(); c != nil {
		if err := syncthing.Reconcile(c, cfg, d.log); err != nil {
			d.log.Error("engine reconcile failed", "err", err)
		}
		if err := pairing.ProcessPendingFolders(c, cfg, d.log); err != nil {
			d.log.Warn("processing pending folders", "err", err)
		}
	}

	// Re-read state: setup commands (add-target, set-password) write UUIDs
	// and credentials there while we run.
	if fresh, err := config.LoadState(); err == nil {
		// The odometer is authoritative in memory. A state file written by a
		// setup command since our last flush is behind, and adopting its
		// figures here would wind the counter backwards. Read before the lock:
		// the tally has its own.
		bytes, files, since := d.totals()
		// Under the lock, because d.state is what it guards — saveState runs
		// on its own timer and would otherwise be racing this swap.
		d.mu.Lock()
		fresh.IPCToken = d.state.IPCToken // never rotate a live token
		if fresh.InstallID == "" {
			// Belt and braces: the id is on disk, so a state file written by a
			// setup command carries it. But if one ever did not, adopting the
			// empty value would make this machine a stranger to every claim it
			// holds — and it would then stop backing up to its own drives.
			fresh.InstallID = d.state.InstallID
		}
		// And the same treatment for a password the OS keyring would not hand back
		// on this reload but that we are already holding. See
		// State.KeepReachableSecrets: a keyring that stops answering mid-session
		// must not take working share destinations down with it.
		fresh.KeepReachableSecrets(d.state)
		if d.tally != nil {
			fresh.BytesCopiedTotal, fresh.FilesCopiedTotal, fresh.CountingSince = bytes, files, since
		}
		d.state = fresh
		d.mu.Unlock()
		// A renamed destination's clocks arrive this way and nowhere else: the
		// marks in memory are keyed by the OLD name, and the next flush writes
		// memory. Taken before prune below, which is what then drops the old
		// name — in that order, so the pair is moved rather than lost.
		if d.marks != nil && d.marks.fill(fresh) && d.tally != nil {
			d.tally.touch()
		}
	}

	// A machine that is demonstrably set up is RECORDED as set up, once.
	//
	// THE BUG THIS FIXES. state.SetupComplete was only ever written by the
	// browser wizard and by adopt, so an install built with the CLI never had
	// it — and status derived "is this set up" from the live config instead,
	// which is not a fact that only moves one way. Removing the last folder
	// therefore reported a fresh install, and the dashboard threw the
	// first-run wizard over the whole page in front of somebody who had just
	// clicked "Stop protecting" on a machine they had been using for days. It
	// took the Settings panel down with it, for the same reason.
	//
	// Here rather than at startup because a machine can become configured
	// while running — the wizard, adopt, or a CLI add-target landing in the
	// config the watcher is reading.
	//
	// The in-memory flag is checked FIRST so the steady state costs nothing.
	// completeSetup re-reads state.json to avoid clobbering a setup command's
	// concurrent write, and applyConfig runs on every watcher tick — without
	// this guard a configured machine would re-read and re-parse that file
	// every two seconds for ever to learn something it already knows. d.state
	// was refreshed from disk immediately above, so the flag is current.
	if cfg.Configured() && !d.setupDone() {
		if err := d.completeSetup(); err != nil {
			d.log.Warn("could not record that setup is finished; the dashboard may offer the first-run wizard again", "err", err)
		}
	}

	// The slow part, deliberately outside the lock: open each destination and
	// bring its manifest up to date. Copies of what that needs are taken from
	// the state rather than read through d.state as we go, so none of it
	// reaches back for the lock we are trying to leave free.
	d.mu.Lock()
	uuids := maps.Clone(d.state.DriveTargetUUIDs)
	creds := maps.Clone(d.state.ShareCredentials)
	d.mu.Unlock()
	prepared := d.prepareTargets(cfg, uuids, creds)

	d.mu.Lock()
	defer d.mu.Unlock()
	if d.cancel != nil {
		d.cancel()
	}
	engCtx, cancel := context.WithCancel(ctx)
	d.cancel = cancel
	d.cfg = cfg
	d.engines = nil
	d.reclaimers = map[string]*localmirror.Reclaimer{}
	for _, b := range d.statusPageBackends {
		_ = b.backend.Close()
	}
	d.statusPageBackends = nil

	// A folder or destination that has just been removed must not leave its
	// last-sync clock behind in state.json for ever.
	if d.marks.prune(cfg) {
		d.tally.touch()
	}

	// This installation's identity, snapshotted for the engines built below.
	//
	// A SNAPSHOT RATHER THAN A CLOSURE OVER d.state, which is swapped wholesale
	// every couple of seconds. Reading the live state from an engine goroutine
	// would mean taking d.mu on the sync path, and d.mu being takeable is the
	// watchdog's entire definition of a live daemon. The values are safe to copy:
	// the install id is minted once and never rotated, and the inherited list
	// only changes during adoption, which refuses to run on a machine that has
	// anything configured — so no engine exists at that moment anyway.
	installID := d.state.InstallID
	inherited := append([]string(nil), d.state.InheritedInstallIDs...)
	owns := (&config.State{InstallID: installID, InheritedInstallIDs: inherited}).Owns

	d.descriptions = map[string]string{}
	for _, p := range prepared {
		t := p.target
		if p.description != "" {
			d.descriptions[t.Name] = p.description
		}
		// One reclaimer per DESTINATION, shared by every folder writing to it:
		// several folders can hit a full disk at the same instant, and they
		// must not all start deleting history concurrently.
		minFree := cfg.MinFreeBytes(t)
		reclaimer := localmirror.NewReclaimer()
		d.reclaimers[t.Name] = reclaimer
		if p.statusBackend != nil {
			d.statusPageBackends = append(d.statusPageBackends, namedBackend{
				name: t.Name, where: status.TargetLocation(t), uuid: p.uuid, backend: p.statusBackend,
			})
		}
		for _, pf := range p.folders {
			f := pf.folder
			var ignores []string
			if !f.NoDefaultIgnores {
				ignores = append(ignores, cfg.Defaults.Ignore...)
			}
			ignores = append(ignores, f.ExtraIgnore...)
			scanMark := d.marks.scanMark(f.ID, t.Name, p.uuid)
			e := localmirror.New(localmirror.Options{
				FolderID:     f.ID,
				TargetName:   t.Name,
				TargetType:   t.Type,
				SourcePath:   f.Path,
				Backend:      pf.backend,
				MachineName:  cfg.General.MachineName,
				Label:        f.Label,
				UUID:         p.uuid,
				MaxAgeDays:   cfg.Defaults.VersioningMaxAgeDays,
				Verify:       pf.verify,
				OfflinePoll:  pf.offlinePoll,
				MinFreeBytes: minFree,
				Reclaimer:    reclaimer,
				Counted:      d.countCopied,
				// The clock this pair was last synced at, carried across the
				// restart, and where the next one is reported back to.
				LastSync: d.marks.lastSync(f.ID, t.Name),
				Synced:   d.syncRecorder(f.ID, t.Name),
				// What the last COMPLETED pass over this pair learned, so this
				// one does not start by re-learning it. Chiefly the timestamp
				// calibration: without it, a destination that does not preserve
				// mtimes recopies every same-size file it holds, once per
				// daemon restart.
				PrevScanStart: scanMark.PassStart,
				MtimeTrusted:  scanMark.MtimeTrusted,
				DestFileCount: scanMark.DestFiles,
				PassCompleted: d.passRecorder(f.ID, t.Name, p.uuid),
				Ignores:       ignores,
				Log:           d.log,
				// What tells this computer's folder on the destination from
				// another computer's that happens to have the same name.
				InstallID: installID,
				Owns:      owns,
			})
			d.engines = append(d.engines, e)
			go e.Run(engCtx)
		}
	}
}

// preparedTarget is one destination's share of a config apply: the connections
// it needs and the manifest refresh, all of it already done. Splitting it out
// is what lets applyConfig do the talking-to-storage part with d.mu free.
type preparedTarget struct {
	target config.Target
	uuid   string
	// statusBackend is a separate connection for the status page, so writing
	// it never contends with a sync in progress. nil when the destination
	// could not be opened at all — the folders below may still have theirs.
	statusBackend localmirror.Backend
	// description is what this destination's own marker file says it physically
	// is. Read here because this is where a live connection already exists and
	// the marker is already being consulted — asking again on the status cycle
	// would be a round trip a minute for a sentence that changes when somebody
	// types one.
	description string
	folders     []preparedFolder
}

// preparedFolder is one folder→destination pair with its own open connection,
// waiting to be handed to an engine.
type preparedFolder struct {
	folder      config.Folder
	backend     localmirror.Backend
	offlinePoll time.Duration
	verify      bool
}

// prepareTargets opens every drive/share destination in the config and refreshes
// each one's adoption manifest.
//
// MUST BE CALLED WITHOUT d.mu HELD. This is the only part of a config apply that
// waits on hardware or the network, and the whole point of separating it is that
// the daemon stays demonstrably alive — lock obtainable, watchdog fed — while a
// destination that is switched off runs down its timeouts. It takes the UUIDs
// and share credentials as arguments for the same reason: reading them back off
// d.state would need the lock this is written to avoid.
func (d *daemon) prepareTargets(cfg *config.Config, uuids, creds map[string]string) []preparedTarget {
	var prepared []preparedTarget
	for _, t := range cfg.Targets {
		if t.Type != "drive" && t.Type != "share" {
			continue
		}
		uuid := uuids[t.Name]
		if uuid == "" {
			d.log.Error("target has no recorded UUID; re-add it", "target", t.Name)
			continue
		}
		p := preparedTarget{target: t, uuid: uuid}
		if sb, _, _, berr := d.buildBackend(t, creds); berr == nil {
			// Refresh the adoption manifest while we have a live connection.
			d.refreshManifest(t, sb, cfg, uuid)
			// And read what the storage says it is. Best-effort: a destination
			// that is not there simply has nothing to say about itself, which is
			// the honest answer rather than a remembered one.
			if m, merr := localmirror.ReadMarker(sb); merr == nil {
				p.description = m.Description
			}
			p.statusBackend = sb
		}
		for _, f := range cfg.FoldersForTarget(t) {
			backend, offlinePoll, verify, err := d.buildBackend(t, creds)
			if err != nil {
				d.log.Error("cannot open target", "target", t.Name, "err", err)
				break
			}
			p.folders = append(p.folders, preparedFolder{
				folder: f, backend: backend, offlinePoll: offlinePoll, verify: verify,
			})
		}
		prepared = append(prepared, p)
	}
	return prepared
}

// refreshManifest rewrites the adoption manifest at a destination root, so a
// fresh install can recover this whole configuration from the storage alone.
//
// Best-effort: an unwritable destination just keeps its older copy until the
// next apply reaches it. Storage we do not recognize gets nothing at all — the
// manifest names this machine and its source folder paths, and mayWrite is what
// keeps that off a drive we would refuse to back up to.
//
// t.Name is passed through so the manifest describes THIS destination and
// summarises the others; without it every drive would carry the addresses of
// all of them. See setup.BuildManifest.
func (d *daemon) refreshManifest(t config.Target, b localmirror.Backend, cfg *config.Config, uuid string) {
	// mayWriteAs, not mayWrite: the manifest lives inside this machine's own
	// directory now, so "may we write here" includes "is that directory ours".
	if !d.mayWriteAs(t.Name, status.TargetLocation(t), b, uuid, config.MachineDir(cfg.General.MachineName)) {
		return
	}
	if err := setup.WriteManifest(b, cfg, d.state.DriveTargetUUIDs, d.state.InstallID, t.Name); err != nil {
		d.log.Warn("could not write adoption manifest", "target", t.Name, "err", err)
	}
}

// buildBackend opens the destination filesystem for a drive or share target.
// Each engine gets its own backend instance (one SMB session per engine).
//
// Share credentials are passed in rather than read from d.state, because
// opening a share dials the network and every caller here is deliberately
// running with the central lock free.
func (d *daemon) buildBackend(t config.Target, creds map[string]string) (localmirror.Backend, time.Duration, bool, error) {
	if d.newBackend != nil {
		return d.newBackend(t, creds)
	}
	switch t.Type {
	case "share":
		pass, ok := creds[t.Name]
		if !ok {
			return nil, 0, false, fmt.Errorf("no stored credentials; run: backup-maker set-password %s", t.Name)
		}
		fs, err := smbfs.New(t.URL, t.Username, pass)
		if err != nil {
			return nil, 0, false, err
		}
		verify := t.Verify == nil || *t.Verify
		return fs, 30 * time.Second, verify, nil
	default:
		return localmirror.NewLocalFS(t.Path), 5 * time.Second, t.Verify != nil && *t.Verify, nil
	}
}

// watchConfigFile reloads config.toml when the CLI (or the user) edits it,
// then reapplies everything. Polling keeps it simple and cross-platform.
func (d *daemon) watchConfigFile(ctx context.Context) {
	path, err := config.ConfigPath()
	if err != nil {
		return
	}
	var lastMod time.Time
	if fi, err := os.Stat(path); err == nil {
		lastMod = fi.ModTime()
	}
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			fi, err := os.Stat(path)
			if err != nil || !fi.ModTime().After(lastMod) {
				continue
			}
			lastMod = fi.ModTime()
			cfg, err := config.Load()
			if err != nil {
				d.log.Error("config reload failed; keeping previous config", "err", err)
				continue
			}
			d.log.Info("config changed; reapplying")
			d.applyConfig(ctx, cfg)
			// Deliberately out here rather than inside applyConfig: bringing
			// the network view up binds a socket, and applyConfig holds the
			// central lock across its whole body. It belongs on the watcher
			// path all the same — config.toml is a documented way to configure
			// this program, and a lan_view edited by hand has to take effect
			// the same way every other key does.
			d.applyLANView(cfg)
		}
	}
}

// eventLoop reacts to syncthing events: folder offers from paired sources,
// receive-only drift, and device connects. Started only once the (lazy)
// engine is running.
func (d *daemon) eventLoop(ctx context.Context) {
	c := d.engineClient()
	if c == nil {
		return
	}
	syncthing.StreamEvents(ctx, c, d.log, func(ev syncthing.Event) {
		switch ev.Type {
		case "PendingFoldersChanged", "DeviceConnected", "ClusterConfigReceived":
			if err := pairing.ProcessPendingFolders(c, d.currentCfg(), d.log); err != nil {
				d.log.Warn("processing pending folders", "err", err)
			}
		case "ConfigSaved":
			if err := syncthing.EnforceReceiveOnly(c, d.currentCfg(), d.log); err != nil {
				d.log.Warn("enforcing receive-only", "err", err)
			}
		}
	})
}

// applyLANView starts or stops the read-only network view to match the config.
//
// It exists because that view is now a dashboard toggle. A setting that only
// takes effect after a restart, presented as a switch, is a switch that lies —
// and this one has to be believed in both directions: somebody turning it OFF
// is withdrawing an unauthenticated listener from their network, and "it will
// stop next time you reboot" is not an answer.
//
// Idempotent, so the config watcher re-applying the same config costs nothing.
// Never called with d.mu held: it binds a socket.
func (d *daemon) applyLANView(cfg *config.Config) {
	d.lanMu.Lock()
	defer d.lanMu.Unlock()
	if d.srv == nil {
		return // before the web server exists; startup calls this again
	}
	if !cfg.General.LANView {
		d.lanErr = ""
		if d.lanView != nil {
			d.lanView.Close()
			d.lanView = nil
			d.log.Info("read-only network view stopped")
		}
		return
	}
	if d.lanView != nil {
		return // already running
	}
	iface, err := lanaddr.Primary()
	if err != nil {
		d.lanErr = "no network address was found on this machine"
		d.log.Warn("network view requested but no LAN address found", "err", err)
		return
	}
	view, err := d.srv.StartLANView(iface.IP, cfg.LANViewPort())
	if err != nil {
		d.lanErr = err.Error()
		d.log.Error("could not start the network view", "err", err)
		return
	}
	d.lanErr = ""
	d.lanView = view
	d.log.Info("read-only network view listening",
		"url", view.URL(), "interface", iface.Name, "mac", iface.MAC,
		"note", "reserve this address on your router to keep the URL stable")
	if !iface.Wired {
		d.log.Warn("the network view is on wifi; a wired connection is steadier for a machine other devices watch")
	}
}

func (d *daemon) stopLANView() {
	d.lanMu.Lock()
	defer d.lanMu.Unlock()
	if d.lanView != nil {
		d.lanView.Close()
		d.lanView = nil
	}
}

// lanViewURL reports where the network view is listening, or "" when it is off.
// The dashboard shows it so the address can be typed into a phone.
func (d *daemon) lanViewURL() string {
	d.lanMu.Lock()
	defer d.lanMu.Unlock()
	if d.lanView == nil {
		return ""
	}
	return d.lanView.URL()
}

// lanViewProblem reports why the view is switched on but not listening, or ""
// when there is nothing wrong.
func (d *daemon) lanViewProblem() string {
	d.lanMu.Lock()
	defer d.lanMu.Unlock()
	return d.lanErr
}

// setSettings writes the [general] changes the dashboard asked for, then brings
// the network view into line immediately rather than waiting for the config
// watcher's next tick — the toggle should be true the moment it is answered.
func (d *daemon) setSettings(req webui.SettingsRequest) error {
	if _, err := setup.ApplySettings(setup.Settings{
		DesktopAlerts:       req.DesktopAlerts,
		BackupsStopped:      req.BackupsStopped,
		SnapshotFailed:      req.SnapshotFailed,
		UnrecognisedStorage: req.UnrecognisedStorage,
		PairRequests:        req.PairRequests,
		LANView:             req.LANView,
		LANViewAccess:       req.LANViewAccess,
		WebhookEnabled:      req.WebhookEnabled,
		WebhookMinimal:      req.WebhookMinimal,
		NtfyEnabled:         req.NtfyEnabled,
		NtfyMinimal:         req.NtfyMinimal,
		UpdateCheck:         req.UpdateCheck,
	}); err != nil {
		return err
	}
	// These land in state.json, not config.toml — they are credentials.
	if req.WebhookURL != nil {
		if err := setup.SetWebhookURL(*req.WebhookURL); err != nil {
			return err
		}
		d.mu.Lock()
		d.state.WebhookURL = strings.TrimSpace(*req.WebhookURL)
		d.mu.Unlock()
	}
	if req.NtfyTopicURL != nil {
		if err := setup.SetNtfyTopicURL(*req.NtfyTopicURL); err != nil {
			return err
		}
		d.mu.Lock()
		d.state.NtfyTopicURL = strings.TrimSpace(*req.NtfyTopicURL)
		d.mu.Unlock()
	}
	if req.NtfyToken != nil {
		if err := setup.SetNtfyToken(*req.NtfyToken); err != nil {
			return err
		}
		d.mu.Lock()
		d.state.NtfyToken = strings.TrimSpace(*req.NtfyToken)
		d.mu.Unlock()
	}
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	d.alerts.setEnabled(anyDeliveryOn(cfg))
	d.alerts.setKinds(cfg.General.Alerts)
	// The URL may have changed in the same request, so the sinks are rebuilt
	// here too rather than waiting for the config watcher's next tick.
	d.alerts.setNotifier(d.deliverySinks(cfg))
	d.applyLANView(cfg)
	// Publish it to the status the dashboard reads, NOW. The config watcher
	// would get here on its own within a couple of seconds, and in that gap
	// /api/status still describes the old settings — so the panel repaints from
	// the daemon and puts the box back the way it was, in front of somebody who
	// has just clicked it. They click again, and switch it back off.
	d.mu.Lock()
	d.cfg = cfg
	d.mu.Unlock()
	return nil
}

// testAlert delivers one notification by the named method, so the dashboard can
// answer "would I actually hear about it?" without waiting for a real fault.
//
// An unknown method is refused plainly rather than answered with a success that
// means nothing: a test button that reports OK for a sink that does not exist is
// worse than no button.
func (d *daemon) testAlert(ctx context.Context, method string) error {
	var sink notify.Notifier
	switch method {
	case "", "desktop":
		sink = notify.Desktop()
	case "webhook":
		w, err := d.webhookSink()
		if err != nil {
			return err
		}
		sink = w
	case "ntfy":
		n, err := d.ntfySink()
		if err != nil {
			return err
		}
		sink = n
	default:
		return fmt.Errorf("no such delivery method: %q", method)
	}
	err := d.alerts.testDeliver(ctx, sink)
	// Recorded like any other delivery: pressing Test is the same question the
	// panel answers underneath, asked deliberately, and its answer should not
	// disagree with what is shown a line below the button.
	if method == "" {
		method = "desktop"
	}
	d.delivery.record([]notify.Result{{Method: method, Err: err, At: time.Now()}})
	if err != nil {
		return fmt.Errorf("the notification could not be delivered: %w", err)
	}
	return nil
}

// webhookSink builds the webhook from the live config and state, or explains
// why it cannot. Errors say which of the two halves is missing, because
// "switched on but no URL" and "URL set but switched off" are different
// mistakes with different fixes.
func (d *daemon) webhookSink() (notify.Notifier, error) {
	cfg := d.currentCfg()
	d.mu.Lock()
	url := d.state.WebhookURL
	d.mu.Unlock()
	if strings.TrimSpace(url) == "" {
		return nil, errors.New("no webhook address has been set")
	}
	return notify.Webhook{
		URL:     url,
		Machine: cfg.General.MachineName,
		Minimal: cfg.General.Webhook.Minimal,
	}, nil
}

// ntfySink builds the ntfy publisher from the live config and state. Same shape
// as webhookSink, including the refusal to invent a topic: an unset one is a
// missing half, not a default.
func (d *daemon) ntfySink() (notify.Notifier, error) {
	cfg := d.currentCfg()
	d.mu.Lock()
	topic := d.state.NtfyTopicURL
	token := d.state.NtfyToken
	d.mu.Unlock()
	if strings.TrimSpace(topic) == "" {
		return nil, errors.New("no ntfy topic has been set")
	}
	return notify.Ntfy{
		TopicURL: topic,
		Token:    token,
		Machine:  cfg.General.MachineName,
		Minimal:  cfg.General.Ntfy.Minimal,
	}, nil
}

// anyDeliveryOn reports whether any delivery method is switched on, which is
// what decides whether the alerter runs at all.
//
// ONE FUNCTION RATHER THAN THE EXPRESSION WRITTEN OUT AT EACH CALL SITE. There
// are two call sites and they must never disagree, and the failure mode of
// disagreement is the quiet one: alerts computed but delivered nowhere, or a
// method switched on in a panel that changes nothing until the daemon restarts.
func anyDeliveryOn(cfg *config.Config) bool {
	return cfg.General.DesktopAlerts || cfg.General.Webhook.Enabled || cfg.General.Ntfy.Enabled
}

// deliverySinks assembles every delivery method that is switched on. Rebuilt on
// each config apply so a change takes effect on the next alert rather than the
// next restart.
//
// An empty list is a legitimate state — somebody who has switched everything
// off — and sends nothing rather than falling back to a default they turned
// off on purpose.
func (d *daemon) deliverySinks(cfg *config.Config) notify.Notifier {
	var sinks notify.Multi
	if cfg.General.DesktopAlerts {
		sinks = append(sinks, notify.Sink{Method: "desktop", Notifier: notify.Desktop()})
	}
	if cfg.General.Webhook.Enabled {
		if w, err := d.webhookSink(); err == nil {
			sinks = append(sinks, notify.Sink{Method: "webhook", Notifier: w})
		} else {
			d.log.Warn("the webhook is switched on but cannot be used", "err", err)
		}
	}
	if cfg.General.Ntfy.Enabled {
		if n, err := d.ntfySink(); err == nil {
			sinks = append(sinks, notify.Sink{Method: "ntfy", Notifier: n})
		} else {
			d.log.Warn("ntfy is switched on but cannot be used", "err", err)
		}
	}
	if len(sinks) == 0 {
		return nil
	}
	return sinks
}

// webhookURLSet reports whether a webhook address is stored, without revealing
// it. The dashboard needs to know one exists so it can say so; it must never be
// handed the URL itself, which is a credential.
func (d *daemon) webhookURLSet() bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return strings.TrimSpace(d.state.WebhookURL) != ""
}

// ntfyTopicSet and ntfyTokenSet answer the same question for ntfy, and observe
// the same rule: the panel is told THAT each is stored, never what it is.
func (d *daemon) ntfyTopicSet() bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return strings.TrimSpace(d.state.NtfyTopicURL) != ""
}

func (d *daemon) ntfyTokenSet() bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return strings.TrimSpace(d.state.NtfyToken) != ""
}

// ntfyTopicDisplay and webhookURLDisplay hand the panel scheme+host with the
// rest bulleted, so somebody can see WHICH address is saved and spot a typo in
// it. The secret half never leaves this process — see notify.RedactEndpoint for
// the mistake that made this necessary.
func (d *daemon) ntfyTopicDisplay() string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return notify.RedactEndpoint(d.state.NtfyTopicURL)
}

func (d *daemon) webhookURLDisplay() string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return notify.RedactEndpoint(d.state.WebhookURL)
}

// buildActions is every callback the dashboard and CLI reach the daemon
// through.
//
// A FUNCTION RATHER THAN A LITERAL INSIDE Run, so that a test can hold the
// result and check that none of them is nil. A nil action is not a compile
// error — it is a route that answers 503 the first time somebody presses the
// button, which has now happened twice. See TestEveryActionIsWired.
func buildActions(d *daemon) webui.Actions {
	return webui.Actions{
		Scan: func(ctx context.Context) (any, error) { return discover.Scan(ctx) },
		AddShare: func(req webui.AddShareRequest) error {
			// setup writes config.toml; the config watcher applies it.
			return setup.AddShareTarget(req.URL, req.Username, req.Password, req.Name, req.Verify)
		},
		Wake:         d.WakeNow,
		AcceptPair:   d.acceptPair,
		DeviceID:     d.deviceID,
		RevertFolder: d.revertFolder,
		Machines:     d.listMachines,
		Storage:      d.machineStorage,
		// Seeing an unusable drive is read-only; preparing one is the only
		// action on this API that destroys a filesystem.
		UnusableDrives: d.unusableDrives,
		PrepareDrive:   d.prepareDrive,
		SetupRecipe:    d.setupRecipe,
		CreateBackup:   d.createBackup,
		RemoveFolder:   setup.RemoveFolder,
		StopMirroring:  setup.StopMirroring,
		// The three actions on a stopped folder. Methods rather than bare
		// setup functions because each one has to say what it did in a
		// sentence, and the delete needs the daemon's own way of opening a
		// destination.
		ReenableFolder:       d.reenableFolder,
		DeleteRetiredBackups: d.deleteRetiredBackups,
		ForgetRetired:        d.forgetRetired,
		RemoveTarget:         setup.RemoveTarget,
		RenameTarget:         setup.RenameTarget,
		DescribeTarget:       setup.DescribeTarget,
		SetFolderIgnores:     setup.SetFolderIgnores,
		AddArchive:           d.addArchive,
		CompleteSetup:        d.completeSetup,
		AdoptAllowed:         setup.AdoptAllowed,
		AdoptScan:            d.adoptScan,
		AdoptInspect:         d.adoptInspect,
		AdoptTestShare:       d.adoptTestShare,
		Adopt:                d.adopt,
		SetArchivePassword:   d.setArchivePassword,
		RemoveArchive:        setup.RemoveArchive,
		SetArchivePaused:     setup.SetArchivePaused,
		SetArchiveSchedule:   setup.SetArchiveSchedule,
		SetSettings:          d.setSettings,
		TestAlert:            d.testAlert,
		ApproveLANDevice:     d.approveLANDevice,
		ForgetLANDevice:      d.forgetLANDevice,
		LANGate: &webui.LANGate{
			ApprovedOnly: d.lanViewApprovedOnly,
			Seen:         d.lanDeviceSeen,
		},
	}
}

// targetDescriptions is what each destination's own marker file says it is,
// keyed by target name, for the status collector.
//
// A destination that could not be opened when the config was last applied is
// simply absent: the description is a fact about the storage that is there, and
// remembering one for storage nobody can reach would be this program answering
// for something it cannot see.
func (d *daemon) targetDescriptions() map[string]string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return maps.Clone(d.descriptions)
}
