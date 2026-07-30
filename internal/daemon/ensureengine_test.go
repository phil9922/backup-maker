// SPDX-License-Identifier: MIT

package daemon

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/phil9922/backup-maker/internal/config"
	"github.com/phil9922/backup-maker/internal/syncthing"
)

// THE ONE TEST THAT RUNS THE REAL ENGINE — deliberately. Every other daemon
// test stubs startEngine, because none of them is about an engine starting and
// the real one downloads a pinned binary and leaves a child process holding
// its own executable. The cost of that seam is that nothing else exercises
// ensureEngine at all: the download-and-verify, the supervisor, the sandboxed
// engine home, the REST handshake. This test walks that whole path on purpose.
//
// Opt-in because the first run fetches the engine over the network and every
// run starts a real child process:
//
//	BM_ENGINE_TEST=1 go test ./internal/daemon -run TestEnsureEngine
func TestEnsureEngineBringsUpARealEngineThatAnswers(t *testing.T) {
	if os.Getenv("BM_ENGINE_TEST") == "" {
		t.Skip("starts (and on first run downloads) the real sync engine; set BM_ENGINE_TEST=1 to run")
	}

	// The developer's machine has usually already paid for the download once.
	// Note where that cached binary lives BEFORE the sandbox moves config.Dir,
	// and seed the sandbox with it — the test is about ensureEngine, not about
	// re-downloading 20MB per run. A first run with no cache still downloads,
	// which is the same path a fresh install takes.
	cached, err := syncthing.BinaryPath()
	if err != nil {
		t.Fatal(err)
	}
	isolateState(t)
	if _, err := os.Stat(cached); err == nil {
		seedEngineBinary(t, cached)
	}

	var logBuf bytes.Buffer
	d := &daemon{
		log:   slog.New(slog.NewTextHandler(&logBuf, nil)),
		state: &config.State{},
		cfg:   &config.Config{General: config.General{MachineName: "my-laptop"}},
	}
	t.Cleanup(func() {
		if t.Failed() {
			t.Logf("daemon log:\n%s", logBuf.String())
		}
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Through engineUp with the seam left nil — the production wiring, so the
	// dispatch to the real implementation is part of what is proved.
	if err := d.engineUp(ctx); err != nil {
		t.Fatalf("ensureEngine: %v", err)
	}

	c := d.engineClient()
	if c == nil {
		t.Fatal("ensureEngine returned nil error but engineClient() is nil; d.sup was never recorded")
	}
	id, err := c.MyID()
	if err != nil {
		t.Fatalf("the engine's REST API does not answer MyID: %v", err)
	}
	if id == "" {
		t.Fatal("the engine answered with an empty device ID; pairing has nothing to show the other machine")
	}

	// A second call must find the running engine and do nothing — that guard is
	// what lets applyConfig call engineUp on every config change.
	if err := d.engineUp(ctx); err != nil {
		t.Fatalf("second engineUp against a running engine: %v", err)
	}

	// The credentials the supervisor generated must have reached state.json —
	// they are what lets the NEXT daemon process talk to its engine.
	onDisk, err := config.LoadState()
	if err != nil {
		t.Fatal(err)
	}
	if onDisk.SyncthingAPIKey == "" || onDisk.SyncthingGUIPort == 0 {
		t.Errorf("engine credentials were not persisted (key %q, port %d); a restarted daemon could not reach its own engine",
			onDisk.SyncthingAPIKey, onDisk.SyncthingGUIPort)
	}

	// Shut down and WAIT for the child to actually die, not merely be told to:
	// Windows refuses to unlink a running executable, so t.TempDir cleanup
	// needs the process gone. The supervisor's WaitDelay gives a stuck child
	// 15s before it is killed outright.
	cancel()
	deadline := time.Now().Add(30 * time.Second)
	for c.Ping() == nil {
		if time.Now().After(deadline) {
			t.Fatal("engine still answering 30s after cancel; the supervisor is not tearing its child down")
		}
		time.Sleep(200 * time.Millisecond)
	}
	// The port closing precedes the process releasing its executable by a
	// moment; give it that moment so the sandbox can be deleted.
	time.Sleep(time.Second)
}

// seedEngineBinary copies an already-downloaded engine binary into the
// sandbox's pinned location, so Ensure finds it instead of downloading.
func seedEngineBinary(t *testing.T, from string) {
	t.Helper()
	dest, err := syncthing.BinaryPath()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		t.Fatal(err)
	}
	src, err := os.Open(from)
	if err != nil {
		t.Fatal(err)
	}
	defer src.Close()
	dst, err := os.OpenFile(dest, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.Copy(dst, src); err != nil {
		dst.Close()
		t.Fatal(err)
	}
	if err := dst.Close(); err != nil {
		t.Fatal(err)
	}
}
