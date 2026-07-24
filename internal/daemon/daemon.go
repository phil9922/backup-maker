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
	"github.com/phil9922/backup-maker/internal/pairing"
	"github.com/phil9922/backup-maker/internal/setup"
	"github.com/phil9922/backup-maker/internal/smbfs"
	"github.com/phil9922/backup-maker/internal/status"
	"github.com/phil9922/backup-maker/internal/syncthing"
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
}

// spaceSample is one destination's last successful usage reading.
type spaceSample struct {
	free, total uint64
	at          time.Time
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
		Cfg:       d.currentCfg,
		Client:    d.engineClient,
		Engines:   d.currentEngines,
		Archives:  d.archiveStatus,
		SetupDone: d.setupDone,
		Space:     d.spaceSamples,
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
		Wake:          d.WakeNow,
		Machines:      d.listMachines,
		Storage:       d.machineStorage,
		CreateBackup:  d.createBackup,
		RemoveFolder:  setup.RemoveFolder,
		RemoveTarget:  setup.RemoveTarget,
		AddArchive:    d.addArchive,
		CompleteSetup: d.completeSetup,
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
			d.statusPageBackends = append(d.statusPageBackends, namedBackend{name: t.Name, backend: sb})
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
				Ignores:      ignores,
				Log:          d.log,
			})
			d.engines = append(d.engines, e)
			go e.Run(engCtx)
		}
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
