// SPDX-License-Identifier: MIT

//go:build linux

package watchdog

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// readOne waits for a single datagram on a socket standing in for systemd's.
func readOne(t *testing.T, c net.PacketConn) string {
	t.Helper()
	if err := c.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 64)
	n, _, err := c.ReadFrom(buf)
	if err != nil {
		t.Fatalf("nothing arrived on the notification socket: %v", err)
	}
	return string(buf[:n])
}

// The form a systemd USER manager normally hands out: a path under /run/user.
func TestDialNotifyWritesToAPathSocket(t *testing.T) {
	path := filepath.Join(t.TempDir(), "notify.sock")
	srv, err := net.ListenPacket("unixgram", path)
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()

	n, err := dialNotify(path)
	if err != nil {
		t.Fatal(err)
	}
	defer n.Close()
	if err := n.Ping(); err != nil {
		t.Fatal(err)
	}
	if got := readOne(t, srv); got != "WATCHDOG=1\n" {
		t.Errorf("sent %q, want %q", got, "WATCHDOG=1\n")
	}
}

// The other form: a leading @ means the ABSTRACT namespace, whose real name
// begins with a NUL byte. Getting this wrong would create a stray file called
// "@..." and every keepalive would vanish into it.
func TestDialNotifyWritesToAnAbstractSocket(t *testing.T) {
	name := "@backup-maker-watchdog-test"
	srv, err := net.ListenPacket("unixgram", name)
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()

	n, err := dialNotify(name)
	if err != nil {
		t.Fatal(err)
	}
	defer n.Close()
	if err := n.Ping(); err != nil {
		t.Fatal(err)
	}
	if got := readOne(t, srv); got != "WATCHDOG=1\n" {
		t.Errorf("sent %q, want %q", got, "WATCHDOG=1\n")
	}
	// Nothing may have been created on disk under the literal name.
	if _, err := os.Stat(name); err == nil {
		os.Remove(name)
		t.Error("the @ form created a file instead of using the abstract namespace")
	}
}

func TestParseUsec(t *testing.T) {
	if got, err := parseUsec("180000000"); err != nil || got != 3*time.Minute {
		t.Errorf("parseUsec(180000000) = %v, %v; want 3m0s, nil", got, err)
	}
	for _, bad := range []string{"", "not-a-number", "0", "-5"} {
		if _, err := parseUsec(bad); err == nil {
			t.Errorf("parseUsec(%q) accepted an unusable interval", bad)
		}
	}
}

// A watchdog interval addressed to a different process — a child that merely
// inherited the environment — is not ours to answer.
func TestOpenIgnoresAnotherProcessesWatchdog(t *testing.T) {
	t.Setenv("WATCHDOG_USEC", "180000000")
	t.Setenv("WATCHDOG_PID", "1")
	t.Setenv("NOTIFY_SOCKET", "/nonexistent/notify.sock")

	n, _, err := open()
	if err != nil {
		t.Fatalf("treated another process's watchdog as an error: %v", err)
	}
	if n != nil {
		t.Error("opened a notification socket for a watchdog addressed elsewhere")
	}
}

// Configured but unreachable is an error, not a silent no-op: it means systemd
// will kill this service once per interval and the journal should say why.
func TestOpenReportsAnUnreachableSocket(t *testing.T) {
	t.Setenv("WATCHDOG_USEC", "180000000")
	t.Setenv("WATCHDOG_PID", "")
	t.Setenv("NOTIFY_SOCKET", filepath.Join(t.TempDir(), "does-not-exist.sock"))

	if _, _, err := open(); err == nil {
		t.Error("a socket that cannot be dialled was reported as fine")
	}
}

// Run must survive that too, rather than taking the daemon down with it.
func TestRunSurvivesAnUnreachableSocket(t *testing.T) {
	t.Setenv("WATCHDOG_USEC", "180000000")
	t.Setenv("WATCHDOG_PID", "")
	t.Setenv("NOTIFY_SOCKET", filepath.Join(t.TempDir(), "does-not-exist.sock"))

	done := make(chan struct{})
	go func() {
		defer close(done)
		Run(context.Background(), func() bool { return true }, quietLog())
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Run blocked on a socket it could not dial")
	}
}
