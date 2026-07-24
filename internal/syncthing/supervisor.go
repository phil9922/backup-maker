// SPDX-License-Identifier: MIT

package syncthing

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/phil9922/backup-maker/internal/config"
)

// Supervisor owns backup-maker's private syncthing instance: isolated home
// dir, loopback GUI with our API key, restart-on-crash.
type Supervisor struct {
	log     *slog.Logger
	binPath string
	homeDir string
	apiKey  string
	guiPort int
	Client  *Client
}

// NewSupervisor prepares (but does not start) the syncthing child. It ensures
// the binary, home dir, API key, and GUI port exist, persisting them in state.
func NewSupervisor(state *config.State, log *slog.Logger) (*Supervisor, error) {
	bin, err := Ensure(log)
	if err != nil {
		return nil, err
	}
	dir, err := config.Dir()
	if err != nil {
		return nil, err
	}
	home := filepath.Join(dir, "syncthing-home")
	if err := os.MkdirAll(home, 0o700); err != nil {
		return nil, err
	}

	changed := false
	if state.SyncthingAPIKey == "" {
		state.SyncthingAPIKey = config.NewToken()
		changed = true
	}
	if state.SyncthingGUIPort == 0 || !portFree(state.SyncthingGUIPort) {
		port, err := freePort()
		if err != nil {
			return nil, err
		}
		state.SyncthingGUIPort = port
		changed = true
	}
	if changed {
		if err := state.Save(); err != nil {
			return nil, err
		}
	}

	s := &Supervisor{
		log:     log.With("sub", "syncthing"),
		binPath: bin,
		homeDir: home,
		apiKey:  state.SyncthingAPIKey,
		guiPort: state.SyncthingGUIPort,
	}
	s.Client = NewClient(s.guiPort, s.apiKey)
	return s, nil
}

// Run starts syncthing and keeps it alive until ctx is cancelled.
func (s *Supervisor) Run(ctx context.Context) error {
	backoff := time.Second
	for {
		start := time.Now()
		err := s.runOnce(ctx)
		if ctx.Err() != nil {
			return nil
		}
		if time.Since(start) > time.Minute {
			backoff = time.Second // ran fine for a while; reset
		}
		s.log.Warn("engine exited; restarting", "err", err, "backoff", backoff)
		select {
		case <-time.After(backoff):
		case <-ctx.Done():
			return nil
		}
		if backoff < time.Minute {
			backoff *= 2
		}
	}
}

func (s *Supervisor) runOnce(ctx context.Context) error {
	logDir, err := config.LogDir()
	if err != nil {
		return err
	}
	logFile, err := os.OpenFile(filepath.Join(logDir, "syncthing.log"),
		os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	defer logFile.Close()

	// v2 CLI shape: global --home before the subcommand; the default-folder
	// opt-out is env-only.
	cmd := exec.CommandContext(ctx, s.binPath,
		"--home="+s.homeDir,
		"serve",
		"--gui-address=127.0.0.1:"+fmt.Sprint(s.guiPort),
		"--gui-apikey="+s.apiKey,
		"--no-browser",
		"--no-restart",
		"--no-upgrade",
	)
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	cmd.Env = append(os.Environ(), "STNOUPGRADE=1", "STNODEFAULTFOLDER=1")
	cmd.Cancel = func() error { return terminate(cmd) }
	cmd.WaitDelay = 15 * time.Second

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("starting %s: %w", s.binPath, err)
	}
	s.log.Info("engine started", "pid", cmd.Process.Pid, "gui_port", s.guiPort)
	return cmd.Wait()
}

// WaitReady polls until the REST API answers or the deadline passes.
func (s *Supervisor) WaitReady(ctx context.Context, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if err := s.Client.Ping(); err == nil {
			return nil
		}
		select {
		case <-time.After(500 * time.Millisecond):
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return fmt.Errorf("sync engine API not ready after %s (see logs/syncthing.log)", timeout)
}

func freePort() (int, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer ln.Close()
	return ln.Addr().(*net.TCPAddr).Port, nil
}

func portFree(port int) bool {
	ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		return false
	}
	ln.Close()
	return true
}
