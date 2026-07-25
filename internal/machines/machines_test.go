// SPDX-License-Identifier: MIT

package machines

import (
	"context"
	"errors"
	"net"
	"slices"
	"strings"
	"syscall"
	"testing"

	"github.com/phil9922/backup-maker/internal/config"
	"github.com/phil9922/backup-maker/internal/discover"
)

// testConfig builds a config literal rather than config.New(), whose
// MachineName comes from the real hostname and would vary per runner.
func testConfig(machineName string, targets ...config.Target) *config.Config {
	return &config.Config{
		General: config.General{MachineName: machineName},
		Targets: targets,
	}
}

func deviceTarget(name, deviceID string) config.Target {
	return config.Target{Type: "device", Name: name, DeviceID: deviceID}
}

// scanning returns a scan func that yields a fixed result, so List's injected
// seam is exercised without touching the network.
func scanning(hosts []discover.Host, err error) func(context.Context) ([]discover.Host, error) {
	return func(context.Context) ([]discover.Host, error) { return hosts, err }
}

// ids flattens a machine list to its IDs, which is what the UI passes back and
// therefore the part worth pinning down.
func ids(ms []Machine) []string {
	out := make([]string, 0, len(ms))
	for _, m := range ms {
		out = append(out, m.ID)
	}
	return out
}

func TestListWithoutScanSkipsTheNetwork(t *testing.T) {
	cfg := testConfig("laptop",
		deviceTarget("omen", "DEV-OMEN"),
		config.Target{Type: "drive", Name: "sd-card", Path: "/media/sd"},
		config.Target{Type: "share", Name: "nas", URL: "//nas/backups"},
	)

	got, err := List(context.Background(), cfg, nil)
	if err != nil {
		t.Fatalf("List with nil scan: %v", err)
	}
	// Only this machine and the paired device: a nil scan is not an error, and
	// drive/share targets are not machines.
	want := []string{KindThis, "DEV-OMEN"}
	if !slices.Equal(ids(got), want) {
		t.Errorf("ids = %v, want %v", ids(got), want)
	}
}

func TestListSourceOrder(t *testing.T) {
	cfg := testConfig("laptop",
		deviceTarget("omen", "DEV-OMEN"),
		deviceTarget("pi", "DEV-PI"),
	)
	scan := scanning([]discover.Host{
		{Name: "nas", Addr: "192.168.1.10"},
		{Name: "router", Addr: "192.168.1.1"},
	}, nil)

	got, err := List(context.Background(), cfg, scan)
	if err != nil {
		t.Fatalf("List: %v", err)
	}

	// This machine first, then scanned hosts in the order scan returned them,
	// then paired machines in config order.
	want := []string{KindThis, "192.168.1.10", "192.168.1.1", "DEV-OMEN", "DEV-PI"}
	if !slices.Equal(ids(got), want) {
		t.Fatalf("ids = %v, want %v", ids(got), want)
	}
	wantKinds := []string{KindThis, KindSMB, KindSMB, KindPaired, KindPaired}
	for i, k := range wantKinds {
		if got[i].Kind != k {
			t.Errorf("machine %d (%s) kind = %q, want %q", i, got[i].ID, got[i].Kind, k)
		}
	}
}

// A failed or partial scan must not cost the caller the machines it could
// still list: the error rides alongside a fully assembled result. Turning this
// into an early return would silently break the "this machine + already-paired
// machines" case whenever the LAN sweep hiccups.
func TestListReturnsMachinesAlongsideScanError(t *testing.T) {
	scanErr := errors.New("sweep failed")

	cases := []struct {
		name  string
		hosts []discover.Host
		want  []string
	}{
		{
			name:  "no hosts and an error",
			hosts: nil,
			want:  []string{KindThis, "DEV-OMEN"},
		},
		{
			name:  "partial hosts and an error",
			hosts: []discover.Host{{Name: "nas", Addr: "192.168.1.10"}},
			want:  []string{KindThis, "192.168.1.10", "DEV-OMEN"},
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			cfg := testConfig("laptop", deviceTarget("omen", "DEV-OMEN"))

			got, err := List(context.Background(), cfg, scanning(c.hosts, scanErr))

			if !errors.Is(err, scanErr) {
				t.Errorf("err = %v, want %v", err, scanErr)
			}
			if !slices.Equal(ids(got), c.want) {
				t.Errorf("ids = %v, want %v", ids(got), c.want)
			}
		})
	}
}

func TestListHostNameFallsBackToAddr(t *testing.T) {
	cases := []struct {
		name string
		host discover.Host
		want string
	}{
		{"a real name is kept", discover.Host{Name: "nas", Addr: "192.168.1.10"}, "nas"},
		{"an empty name falls back to the address", discover.Host{Name: "", Addr: "192.168.1.10"}, "192.168.1.10"},
		{"a name equal to the address stays the address", discover.Host{Name: "192.168.1.10", Addr: "192.168.1.10"}, "192.168.1.10"},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := List(context.Background(), testConfig("laptop"), scanning([]discover.Host{c.host}, nil))
			if err != nil {
				t.Fatalf("List: %v", err)
			}
			if len(got) != 2 {
				t.Fatalf("got %d machines, want 2", len(got))
			}
			if got[1].Name != c.want {
				t.Errorf("Name = %q, want %q", got[1].Name, c.want)
			}
			if got[1].Addr != c.host.Addr {
				t.Errorf("Addr = %q, want %q", got[1].Addr, c.host.Addr)
			}
		})
	}
}

func TestListCarriesNeedsAuthFromScan(t *testing.T) {
	got, err := List(context.Background(), testConfig("laptop"), scanning([]discover.Host{
		{Name: "open", Addr: "192.168.1.10"},
		{Name: "locked", Addr: "192.168.1.11", NeedsAuth: true},
	}, nil))
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if got[1].NeedsAuth {
		t.Error("guest-listable host reported NeedsAuth")
	}
	if !got[2].NeedsAuth {
		t.Error("locked host did not report NeedsAuth")
	}
}

func TestListBrowsable(t *testing.T) {
	got, err := List(context.Background(), testConfig("laptop", deviceTarget("omen", "DEV-OMEN")),
		scanning([]discover.Host{{Name: "nas", Addr: "192.168.1.10"}}, nil))
	if err != nil {
		t.Fatalf("List: %v", err)
	}

	cases := []struct {
		idx  int
		want bool
	}{
		{0, true},  // this machine: its drives can be listed
		{1, true},  // SMB host: its shares can be listed
		{2, false}, // paired machine: it picks its own destination folder
	}
	for _, c := range cases {
		if got[c.idx].Browsable != c.want {
			t.Errorf("%s (%s): Browsable = %v, want %v", got[c.idx].ID, got[c.idx].Kind, got[c.idx].Browsable, c.want)
		}
	}
	// A non-browsable machine has to say why, or the UI can only show a dead end.
	if got[2].Note == "" {
		t.Error("paired machine has no Note explaining why it isn't browsable")
	}
}

func TestListSkipsNonDeviceTargets(t *testing.T) {
	cfg := testConfig("laptop",
		config.Target{Type: "drive", Name: "sd-card", Path: "/media/sd"},
		config.Target{Type: "share", Name: "nas-backups", URL: "//nas/backups"},
		deviceTarget("omen", "DEV-OMEN"),
		config.Target{Type: "drive", Name: "usb", Path: "/media/usb"},
	)

	got, err := List(context.Background(), cfg, nil)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	// Drives and shares are storage ON a machine, not machines of their own.
	want := []string{KindThis, "DEV-OMEN"}
	if !slices.Equal(ids(got), want) {
		t.Errorf("ids = %v, want %v", ids(got), want)
	}
}

// List does no cross-source merging, and that is the contract: a paired
// machine and a scanned SMB host can be the same physical computer and still
// appear twice, because they are reached in different ways and offer different
// storage. Duplicate DeviceIDs pass through untouched for the same reason —
// deciding which entry "wins" is not this function's job.
func TestListDoesNotDedupeAcrossSources(t *testing.T) {
	cfg := testConfig("laptop",
		deviceTarget("omen", "DEV-OMEN"),
		deviceTarget("omen (second config entry)", "DEV-OMEN"),
	)
	// The same machine the paired device entry refers to, also reachable over SMB.
	scan := scanning([]discover.Host{{Name: "omen", Addr: "192.168.1.20"}}, nil)

	got, err := List(context.Background(), cfg, scan)
	if err != nil {
		t.Fatalf("List: %v", err)
	}

	want := []string{KindThis, "192.168.1.20", "DEV-OMEN", "DEV-OMEN"}
	if !slices.Equal(ids(got), want) {
		t.Fatalf("ids = %v, want %v", ids(got), want)
	}
	// The SMB entry and the paired entry share a name but remain separate
	// machines with different kinds.
	if got[1].Name != "omen" || got[2].Name != "omen" {
		t.Errorf("names = %q, %q, want both %q", got[1].Name, got[2].Name, "omen")
	}
	if got[1].Kind == got[2].Kind {
		t.Errorf("scanned and paired entries collapsed to kind %q", got[1].Kind)
	}
}

func TestListWithNoTargets(t *testing.T) {
	cases := []struct {
		name    string
		targets []config.Target
	}{
		{"nil targets", nil},
		{"empty targets", []config.Target{}},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			cfg := &config.Config{
				General: config.General{MachineName: "laptop"},
				Targets: c.targets,
			}
			got, err := List(context.Background(), cfg, nil)
			if err != nil {
				t.Fatalf("List: %v", err)
			}
			if !slices.Equal(ids(got), []string{KindThis}) {
				t.Fatalf("ids = %v, want [%s]", ids(got), KindThis)
			}
			if got[0].Name != "laptop" {
				t.Errorf("Name = %q, want %q", got[0].Name, "laptop")
			}
		})
	}
}

// An unnamed machine is passed through unnamed. Inventing a fallback here
// would hide a config problem the caller is better placed to fix.
func TestListPassesEmptyMachineNameThrough(t *testing.T) {
	got, err := List(context.Background(), testConfig(""), nil)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if got[0].Name != "" {
		t.Errorf("Name = %q, want empty", got[0].Name)
	}
	if got[0].ID != KindThis {
		t.Errorf("ID = %q, want %q", got[0].ID, KindThis)
	}
}

func TestListPassesContextToScan(t *testing.T) {
	type key struct{}
	ctx := context.WithValue(context.Background(), key{}, "marker")

	var seen bool
	scan := func(got context.Context) ([]discover.Host, error) {
		seen = got.Value(key{}) == "marker"
		return nil, nil
	}
	if _, err := List(ctx, testConfig("laptop"), scan); err != nil {
		t.Fatalf("List: %v", err)
	}
	if !seen {
		t.Error("scan did not receive the caller's context")
	}
}

// A paired machine chooses its own destination folder, so there is nothing to
// browse. The error has to name the target, because that name is what the user
// has to type on the other machine.
func TestStorageForPairedDeviceRefusesToBrowse(t *testing.T) {
	cases := []struct {
		name       string
		targetName string
		machineID  string
	}{
		{"plain name", "omen", "DEV-OMEN"},
		{"name with spaces", "living room pi", "DEV-PI"},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			cfg := testConfig("laptop",
				config.Target{Type: "drive", Name: "sd-card", Path: "/media/sd"},
				deviceTarget(c.targetName, c.machineID),
			)

			got, err := StorageFor(context.Background(), cfg, c.machineID, "", "")

			if err == nil {
				t.Fatal("browsing a paired machine succeeded, want an error")
			}
			if got != nil {
				t.Errorf("storage = %v, want nil", got)
			}
			if !strings.Contains(err.Error(), c.targetName) {
				t.Errorf("error %q does not name the target %q", err, c.targetName)
			}
			if !strings.Contains(err.Error(), "backup-maker receive enable") {
				t.Errorf("error %q does not say what to do about it", err)
			}
		})
	}
}

// refusedAddr returns a loopback address with nothing listening on it, so an
// SMB dial fails immediately without any network, DNS, or live host.
func refusedAddr(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Skipf("cannot bind loopback TCP: %v", err)
	}
	addr := ln.Addr().String()
	if err := ln.Close(); err != nil {
		t.Fatalf("closing probe listener: %v", err)
	}
	return addr
}

// Anonymous SMB listing fails the same way whether the host is unreachable or
// merely locked, so the hint is added whenever no credentials were supplied.
// It must be added by wrapping: callers still need to inspect the real cause.
func TestStorageForSMBErrorHintsAtCredentials(t *testing.T) {
	addr := refusedAddr(t)

	t.Run("no username gets the hint", func(t *testing.T) {
		got, err := StorageFor(context.Background(), testConfig("laptop"), addr, "", "")
		if err == nil {
			t.Fatal("listing shares on a dead address succeeded, want an error")
		}
		if got != nil {
			t.Errorf("storage = %v, want nil", got)
		}
		if !strings.Contains(err.Error(), "needs a username and password") {
			t.Errorf("error %q lacks the credentials hint", err)
		}

		// The hint must not swallow the cause.
		inner := errors.Unwrap(err)
		if inner == nil {
			t.Fatal("errors.Unwrap returned nil: the underlying error was flattened")
		}
		if !strings.Contains(err.Error(), inner.Error()) {
			t.Errorf("wrapped error %q does not contain the cause %q", err, inner)
		}
		var opErr *net.OpError
		if !errors.As(err, &opErr) {
			t.Errorf("errors.As could not reach the *net.OpError through %q", err)
		}
		if !errors.Is(err, syscall.ECONNREFUSED) {
			t.Errorf("errors.Is could not reach ECONNREFUSED through %q", err)
		}
	})

	t.Run("a supplied username gets no hint", func(t *testing.T) {
		_, err := StorageFor(context.Background(), testConfig("laptop"), addr, "phil", "hunter2")
		if err == nil {
			t.Fatal("listing shares on a dead address succeeded, want an error")
		}
		// Credentials were already given; suggesting them again is noise.
		if strings.Contains(err.Error(), "needs a username and password") {
			t.Errorf("error %q suggests credentials that were already supplied", err)
		}
		if !errors.Is(err, syscall.ECONNREFUSED) {
			t.Errorf("errors.Is could not reach ECONNREFUSED through %q", err)
		}
	})
}

// The "this" branch of StorageFor is deliberately untested. It calls the
// package-level browse.Drives() with no injection seam, so on any machine with
// no mounted drives — CI, a container — every per-item assertion is skipped and
// the test passes no matter what the code does. A mislabelled Storage.Kind was
// confirmed to slip through such a test undetected. Testing it needs the seam
// first; see the checklist.
