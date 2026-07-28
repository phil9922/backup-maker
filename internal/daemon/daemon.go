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

	// foreign names the destinations whose storage we do not recognize, so the
	// refusal to write there is reported on the transition rather than once a
	// minute for ever. It has its own lock because the status writer asks the
	// same question while holding d.mu.
	foreignMu sync.Mutex
	foreign   map[string]bool

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

	state, err := config.LoadState()
	if err != nil {
		return fmt.Errorf("loading state: %w", err)
	}
	if state.IPCToken == "" {
		state.IPCToken = config.NewToken()
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
		if err := d.ensureEngine(ctx); err != nil {
			return err
		}
	} else {
		log.Info("no machine targets configured; sync engine stays off (nothing downloaded)")
	}

	d.applyConfig(ctx, cfg)
	go d.watchConfigFile(ctx)
	go d.archiveLoop(ctx)

	collector := &status.Collector{
		Cfg:                d.currentCfg,
		Client:             d.engineClient,
		Engines:            d.currentEngines,
		Archives:           d.archiveStatus,
		HasArchivePassword: d.hasArchivePassword,
		SetupDone:          d.setupDone,
		Space:              d.spaceSamples,
		Totals:             d.totals,
		LANViewURL:         d.lanViewURL,
		LANViewProblem:     d.lanViewProblem,
		LANDevices:         d.lanDevices,
		WebhookURLSet:      d.webhookURLSet,
		NtfyTopicSet:       d.ntfyTopicSet,
		NtfyTokenSet:       d.ntfyTokenSet,
		NtfyTopicDisplay:   d.ntfyTopicDisplay,
		WebhookURLDisplay:  d.webhookURLDisplay,
		Delivery:           d.delivery.snapshot,
	}
	// Wake-on-LAN for offline targets that opted in with a MAC address.
	go d.wakeLoop(ctx, collector.Collect)
	// A status page on each destination, so backups can still be checked when
	// this machine is off.
	go d.statusPageLoop(ctx, collector.Collect)

	actions := webui.Actions{
		Scan: func(ctx context.Context) (any, error) { return discover.Scan(ctx) },
		AddShare: func(req webui.AddShareRequest) error {
			// setup writes config.toml; the config watcher applies it.
			return setup.AddShareTarget(req.URL, req.Username, req.Password, req.Name, req.Verify)
		},
		Wake:               d.WakeNow,
		AcceptPair:         d.acceptPair,
		DeviceID:           d.deviceID,
		RevertFolder:       d.revertFolder,
		Machines:           d.listMachines,
		Storage:            d.machineStorage,
		CreateBackup:       d.createBackup,
		RemoveFolder:       setup.RemoveFolder,
		RemoveTarget:       setup.RemoveTarget,
		SetFolderIgnores:   setup.SetFolderIgnores,
		AddArchive:         d.addArchive,
		CompleteSetup:      d.completeSetup,
		AdoptAllowed:       setup.AdoptAllowed,
		AdoptScan:          d.adoptScan,
		AdoptInspect:       d.adoptInspect,
		AdoptTestShare:     d.adoptTestShare,
		Adopt:              d.adopt,
		SetArchivePassword: d.setArchivePassword,
		SetSettings:        d.setSettings,
		TestAlert:          d.testAlert,
		ApproveLANDevice:   d.approveLANDevice,
		ForgetLANDevice:    d.forgetLANDevice,
		LANGate: &webui.LANGate{
			ApprovedOnly: d.lanViewApprovedOnly,
			Seen:         d.lanDeviceSeen,
		},
	}
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
		if err := d.ensureEngine(ctx); err != nil {
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
		if d.tally != nil {
			fresh.BytesCopiedTotal, fresh.FilesCopiedTotal, fresh.CountingSince = bytes, files, since
		}
		d.state = fresh
		d.mu.Unlock()
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

	for _, p := range prepared {
		t := p.target
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
				Ignores:  ignores,
				Log:      d.log,
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
	folders       []preparedFolder
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
// manifest names this machine, its source folder paths and every destination it
// backs up to, and mayWrite is what keeps that off a drive we would refuse to
// back up to.
func (d *daemon) refreshManifest(t config.Target, b localmirror.Backend, cfg *config.Config, uuid string) {
	if !d.mayWrite(t.Name, status.TargetLocation(t), b, uuid) {
		return
	}
	if err := setup.WriteManifest(b, cfg, d.state.DriveTargetUUIDs); err != nil {
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
