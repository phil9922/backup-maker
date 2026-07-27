// SPDX-License-Identifier: MIT

// Package daemon composes the long-lived backup-maker process: single-instance
// lock, syncthing supervisor, local-mirror engines, and the web UI/API server.
package daemon

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"sync"
	"time"

	"github.com/phil9922/backup-maker/internal/archive"
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
	// minute for ever. It has its own lock because applyConfig asks the same
	// question while holding d.mu.
	foreignMu sync.Mutex
	foreign   map[string]bool
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
	d.alerts = newAlerter(notify.Desktop(), log, cfg.General.DesktopAlerts)
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
	}
	srv, err := webui.New(cfg, state, log, func() any { return collector.Collect() }, actions)
	if err != nil {
		return err
	}
	log.Info("dashboard listening", "url", fmt.Sprintf("http://127.0.0.1:%d", cfg.General.DashboardPort))

	// Optional read-only view for other devices on the network. Opt-in, and
	// never able to change anything: setup stays loopback-only.
	if cfg.General.LANView {
		if iface, ierr := lanaddr.Primary(); ierr != nil {
			log.Warn("network view requested but no LAN address found", "err", ierr)
		} else if view, verr := srv.StartLANView(iface.IP, cfg.LANViewPort()); verr != nil {
			log.Error("could not start the network view", "err", verr)
		} else {
			defer view.Close()
			log.Info("read-only network view listening",
				"url", view.URL(), "interface", iface.Name, "mac", iface.MAC,
				"note", "reserve this address on your router to keep the URL stable")
			if !iface.Wired {
				log.Warn("the network view is on wifi; a wired connection is steadier for a machine other devices watch")
			}
		}
	}

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
func (d *daemon) applyConfig(ctx context.Context, cfg *config.Config) {
	// Before anything below can find unrecognised storage and want to say so.
	d.alerts.setEnabled(cfg.General.DesktopAlerts)
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
		fresh.IPCToken = d.state.IPCToken // never rotate a live token
		// The odometer is authoritative in memory. A state file written by a
		// setup command since our last flush is behind, and adopting its
		// figures here would wind the counter backwards.
		if d.tally != nil {
			fresh.BytesCopiedTotal, fresh.FilesCopiedTotal, fresh.CountingSince = d.tally.snapshot()
		}
		d.state = fresh
	}

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

	for _, t := range cfg.Targets {
		if t.Type != "drive" && t.Type != "share" {
			continue
		}
		uuid := d.state.DriveTargetUUIDs[t.Name]
		if uuid == "" {
			d.log.Error("target has no recorded UUID; re-add it", "target", t.Name)
			continue
		}
		// One reclaimer per DESTINATION, shared by every folder writing to it:
		// several folders can hit a full disk at the same instant, and they
		// must not all start deleting history concurrently.
		minFree := cfg.MinFreeBytes(t)
		reclaimer := localmirror.NewReclaimer()
		d.reclaimers[t.Name] = reclaimer
		// A separate connection for the status page, so writing it never
		// contends with a sync in progress.
		if sb, _, _, berr := d.buildBackend(t); berr == nil {
			// Refresh the adoption manifest while we have a live connection.
			d.refreshManifest(t, sb, cfg, uuid)
			d.statusPageBackends = append(d.statusPageBackends, namedBackend{
				name: t.Name, where: status.TargetLocation(t), uuid: uuid, backend: sb,
			})
		}
		for _, f := range cfg.FoldersForTarget(t) {
			backend, offlinePoll, verify, err := d.buildBackend(t)
			if err != nil {
				d.log.Error("cannot open target", "target", t.Name, "err", err)
				break
			}
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
				Backend:      backend,
				MachineName:  cfg.General.MachineName,
				Label:        f.Label,
				UUID:         uuid,
				MaxAgeDays:   cfg.Defaults.VersioningMaxAgeDays,
				Verify:       verify,
				OfflinePoll:  offlinePoll,
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
func (d *daemon) buildBackend(t config.Target) (localmirror.Backend, time.Duration, bool, error) {
	switch t.Type {
	case "share":
		pass, ok := d.state.ShareCredentials[t.Name]
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
