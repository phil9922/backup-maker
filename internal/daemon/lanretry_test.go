// SPDX-License-Identifier: MIT

package daemon

import (
	"bytes"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"testing"

	"github.com/phil9922/backup-maker/internal/config"
	"github.com/phil9922/backup-maker/internal/lanaddr"
	"github.com/phil9922/backup-maker/internal/webui"
)

// A daemon with a web server on a spare loopback port, so the tests neither
// clash with a real daemon on this machine nor with each other.
func viewDaemon(t *testing.T) (*daemon, *config.Config) {
	t.Helper()
	d := gatedDaemon(t)
	cfg := d.currentCfg()
	cfg.General.DashboardPort = freePort(t)
	cfg.General.LANViewPort = freePort(t)
	srv, err := webui.New(cfg, d.state, slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)),
		func() any { return map[string]any{} }, webui.Actions{})
	if err != nil {
		t.Fatalf("building the web server: %v", err)
	}
	d.srv = srv
	t.Cleanup(srv.Shutdown)
	t.Cleanup(d.stopLANView)
	return d, cfg
}

func freePort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	return ln.Addr().(*net.TCPAddr).Port
}

var loopback = lanaddr.Interface{Name: "lo-test", IP: "127.0.0.1", Wired: true}

// THE BOOT RACE. The daemon starts before wifi has an address, so the first
// attempt to bring the view up fails honestly. It must not be the last
// attempt: once an address exists, the next retry has to find it and listen,
// and the reason shown on the dashboard has to go away with it.
func TestNetworkViewComesUpOnceAnAddressAppears(t *testing.T) {
	d, cfg := viewDaemon(t)
	haveAddress := false
	d.lanPrimary = func() (lanaddr.Interface, error) {
		if !haveAddress {
			return lanaddr.Interface{}, errors.New("no local network interface found")
		}
		return loopback, nil
	}

	d.applyLANView(cfg) // startup
	if d.lanViewURL() != "" {
		t.Fatal("the view came up with no address to bind to")
	}
	if d.lanViewProblem() == "" {
		t.Fatal("a view that failed to start reported no problem")
	}

	d.retryLANViewOnce() // still no address: stays down, keeps saying why
	if d.lanViewURL() != "" || d.lanViewProblem() == "" {
		t.Fatal("a retry with no address changed the outcome")
	}

	haveAddress = true
	d.retryLANViewOnce()
	url := d.lanViewURL()
	if url == "" {
		t.Fatalf("the view did not come up after an address appeared; problem reported: %q", d.lanViewProblem())
	}
	if p := d.lanViewProblem(); p != "" {
		t.Errorf("the view is listening at %s but the dashboard would still show %q", url, p)
	}
	resp, err := http.Get(url + "/")
	if err != nil {
		t.Fatalf("the reported URL %s does not answer: %v", url, err)
	}
	resp.Body.Close()
}

// A view that is listening is left alone: retrying must not look for a new
// address and rebind, or an address change would knock a phone off mid-read
// for no reason. And a view switched OFF is never retried at all — off means
// no listener, and no attempt to make one.
func TestRetryLeavesAWorkingOrSwitchedOffViewAlone(t *testing.T) {
	d, cfg := viewDaemon(t)
	lookups := 0
	d.lanPrimary = func() (lanaddr.Interface, error) { lookups++; return loopback, nil }

	d.applyLANView(cfg)
	if d.lanViewURL() == "" {
		t.Fatalf("the view did not start: %q", d.lanViewProblem())
	}
	d.retryLANViewOnce()
	d.retryLANViewOnce()
	if lookups != 1 {
		t.Errorf("a listening view was looked up again on retry: %d lookups, want 1", lookups)
	}

	cfg.General.LANView = false
	d.applyLANView(cfg)
	if d.lanViewURL() != "" {
		t.Fatal("switching the view off left it listening")
	}
	d.lanPrimary = func() (lanaddr.Interface, error) {
		t.Error("a switched-off view went looking for an address")
		return loopback, nil
	}
	d.retryLANViewOnce()
	if d.lanViewURL() != "" || d.lanViewProblem() != "" {
		t.Error("retrying brought a switched-off view back")
	}
}

// The reason is logged when it is news, not on every tick: a laptop away from
// any network would otherwise write the same line every fifteen seconds.
func TestARepeatedFailureIsLoggedOnce(t *testing.T) {
	d, cfg := viewDaemon(t)
	var logBuf bytes.Buffer
	d.log = slog.New(slog.NewTextHandler(&logBuf, nil))
	d.lanPrimary = func() (lanaddr.Interface, error) {
		return lanaddr.Interface{}, errors.New("no local network interface found")
	}

	d.applyLANView(cfg)
	for range 5 {
		d.retryLANViewOnce()
	}
	if n := bytes.Count(logBuf.Bytes(), []byte("no LAN address found")); n != 1 {
		t.Errorf("the same failure was logged %d times over 6 attempts, want once:\n%s", n, logBuf.String())
	}
}
