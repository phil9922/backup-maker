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

	"github.com/phil9922/backup-maker/internal/browse"
	"github.com/phil9922/backup-maker/internal/config"
	"github.com/phil9922/backup-maker/internal/discover"
	"github.com/phil9922/backup-maker/internal/testpath"
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

// attached returns a Drives func that yields a fixed result, so StorageFor's
// local branch runs on a machine with nothing mounted — CI, a container —
// instead of iterating over an empty list and asserting nothing.
func attached(drives ...browse.Drive) func() []browse.Drive {
	return func() []browse.Drive { return drives }
}

// sharing returns a ListShares func that yields a fixed result, so StorageFor's
// SMB branch runs without a server.
func sharing(shares []string, err error) func(context.Context, string, string, string) ([]string, error) {
	return func(context.Context, string, string, string) ([]string, error) { return shares, err }
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
		config.Target{Type: "drive", Name: "sd-card", Path: testpath.Abs("/media/sd")},
		config.Target{Type: "share", Name: "nas", URL: "//nas/backups"},
	)

	got, err := List(context.Background(), cfg, nil)
	if err != nil {
		t.Fatalf("List with nil scan: %v", err)
	}
	// A nil scan is not an error, and nothing here touches the network: the
	// share appears because it is in the CONFIG, read from disk, not because
	// anybody went looking for it. The drive is still not a machine.
	want := []string{KindThis, "DEV-OMEN", "nas"}
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

// A drive is storage on THIS computer and never a machine of its own. A share
// is storage too — but the computer serving it is a machine, and one this
// config already knows how to reach.
func TestADriveIsNeverAMachineButAConfiguredShareHostIs(t *testing.T) {
	cfg := testConfig("laptop",
		config.Target{Type: "drive", Name: "sd-card", Path: testpath.Abs("/media/sd")},
		config.Target{Type: "share", Name: "nas-backups", URL: "//nas/backups"},
		deviceTarget("omen", "DEV-OMEN"),
		config.Target{Type: "drive", Name: "usb", Path: testpath.Abs("/media/usb")},
	)

	got, err := List(context.Background(), cfg, nil)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	want := []string{KindThis, "DEV-OMEN", "nas"}
	if !slices.Equal(ids(got), want) {
		t.Errorf("ids = %v, want %v", ids(got), want)
	}
}

// THE GUARANTEE: a destination this computer already backs up to can be
// reached without scanning the network for it.
//
// It used to be absent from the list entirely — only paired machines were read
// out of the config — so the only route to a share already in daily use was to
// go hunting for it on the network, where it turned up as a stranger asking
// for a password the daemon had stored all along.
func TestAConfiguredShareIsOfferedWithoutAScan(t *testing.T) {
	cfg := testConfig("laptop", config.Target{
		Type: "share", Name: "backup-pi", URL: "//192.168.5.141/backups", Username: "pk",
	})

	got, err := List(context.Background(), cfg, nil) // nil scan: no network at all
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	var found *Machine
	for i := range got {
		if got[i].Addr == "192.168.5.141" {
			found = &got[i]
		}
	}
	if found == nil {
		t.Fatalf("the configured share is not offered without a scan: %v", ids(got))
	}
	if found.NeedsAuth {
		t.Error("a destination this computer is already using is asking for a password again")
	}
	if !found.Browsable {
		t.Error("the share host is not browsable, so its storage can never be listed")
	}
	if found.Name != "backup-pi" {
		t.Errorf("name = %q, want the name this destination is known by", found.Name)
	}
}

// One machine, not two, when a share is both configured and discovered —
// otherwise the same computer appears twice and only one of the entries knows
// the password.
func TestAShareThatIsBothConfiguredAndScannedAppearsOnce(t *testing.T) {
	cfg := testConfig("laptop", config.Target{
		Type: "share", Name: "backup-pi", URL: "//192.168.5.141/backups",
	})
	scan := scanning([]discover.Host{{Addr: "192.168.5.141", Name: "raspberrypi"}}, nil)

	got, err := List(context.Background(), cfg, scan)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	n := 0
	for _, m := range got {
		if m.Addr == "192.168.5.141" {
			n++
		}
	}
	if n != 1 {
		t.Errorf("the same host appears %d times: %v", n, ids(got))
	}
}

// A port that is not the default has to survive into the machine id, or the
// storage lookup goes to the wrong place.
func TestANonDefaultSharePortSurvivesIntoTheMachineID(t *testing.T) {
	got, err := ShareAddr("//nas.local:4450/backups")
	if err != nil {
		t.Fatal(err)
	}
	if got != "nas.local:4450" {
		t.Errorf("ShareAddr = %q, want host:port", got)
	}
	if plain, err := ShareAddr("//nas.local/backups"); err != nil || plain != "nas.local" {
		t.Errorf("ShareAddr = %q, %v — the default port must not be spelled out", plain, err)
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
				config.Target{Type: "drive", Name: "sd-card", Path: testpath.Abs("/media/sd")},
				deviceTarget(c.targetName, c.machineID),
			)

			got, err := StorageFor(context.Background(), cfg, c.machineID, "", "", nil, nil, nil)

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
		got, err := StorageFor(context.Background(), testConfig("laptop"), addr, "", "", nil, nil, nil)
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
		_, err := StorageFor(context.Background(), testConfig("laptop"), addr, "phil", "hunter2", nil, nil, nil)
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

// Local storage is drives, not shares. Getting Kind wrong here would report
// every disk inside this computer as network storage across the whole UI and
// API, and only injected drives can catch it: with none mounted, a loop over
// the real ones asserts nothing at all.
func TestStorageForLocalDrives(t *testing.T) {
	cfg := testConfig("laptop",
		config.Target{Type: "drive", Name: "sd-card", Path: testpath.Abs("/media/sd")},
		// Neither of these should claim a drive: wrong type, and a share.
		config.Target{Type: "share", Name: "usb-lookalike", URL: testpath.Abs("/media/usb")},
		config.Target{Type: "device", Name: "omen", Path: testpath.Abs("/media/usb")},
	)

	got, err := StorageFor(context.Background(), cfg, KindThis, "", "", attached(
		browse.Drive{Path: testpath.Abs("/media/sd"), Label: "SD Card", Free: 4 << 30, Total: 16 << 30},
		browse.Drive{Path: testpath.Abs("/media/usb"), Label: "USB stick", Free: 1 << 30, Total: 8 << 30},
	), nil, nil)
	if err != nil {
		t.Fatalf("StorageFor(%s): %v", KindThis, err)
	}

	// A drive carries Path and no URL; capacity comes straight from the drive.
	want := []Storage{
		{Kind: "drive", Label: "SD Card", Path: testpath.Abs("/media/sd"), Free: 4 << 30, Total: 16 << 30, ExistingTarget: "sd-card"},
		{Kind: "drive", Label: "USB stick", Path: testpath.Abs("/media/usb"), Free: 1 << 30, Total: 8 << 30},
	}
	if !slices.Equal(got, want) {
		t.Errorf("storage = %+v, want %+v", got, want)
	}
}

func TestStorageForWithNoDrivesAttached(t *testing.T) {
	got, err := StorageFor(context.Background(), testConfig("laptop"), KindThis, "", "", attached(), nil, nil)
	if err != nil {
		t.Fatalf("StorageFor(%s): %v", KindThis, err)
	}
	if len(got) != 0 {
		t.Errorf("storage = %+v, want none", got)
	}
	// The picker renders an empty list; a nil slice would encode as JSON null.
	if got == nil {
		t.Error("storage is nil, want an empty slice")
	}
}

// The SMB branch turns bare share names into addressable URLs and matches them
// against configured share targets case-insensitively, because users type host
// and share names in whatever case they please.
func TestStorageForShares(t *testing.T) {
	cfg := testConfig("laptop",
		config.Target{Type: "share", Name: "nas-backups", URL: "//nas/BACKUPS"},
		config.Target{Type: "drive", Name: "media-lookalike", Path: "//nas/media"},
	)
	capacity := map[string][2]uint64{
		"//nas/backups": {2 << 30, 500 << 30},
		"//nas/media":   {7 << 30, 12 << 30},
	}
	usage := func(url, user, pass string) (uint64, uint64) {
		if user != "phil" || pass != "hunter2" {
			t.Errorf("usage(%q) got credentials %q/%q, want phil/hunter2", url, user, pass)
		}
		c := capacity[url]
		return c[0], c[1]
	}

	got, err := StorageFor(context.Background(), cfg, "nas", "phil", "hunter2", nil, sharing([]string{"backups", "media"}, nil), usage)
	if err != nil {
		t.Fatalf("StorageFor(nas): %v", err)
	}

	// A share carries URL and no Path, keeps the name the server gave it, and
	// gets the capacity looked up for its own URL — not another share's.
	want := []Storage{
		{Kind: "share", Label: "backups", URL: "//nas/backups", Free: 2 << 30, Total: 500 << 30, ExistingTarget: "nas-backups"},
		{Kind: "share", Label: "media", URL: "//nas/media", Free: 7 << 30, Total: 12 << 30},
	}
	if !slices.Equal(got, want) {
		t.Errorf("storage = %+v, want %+v", got, want)
	}
}

// A share that can be listed but not opened still belongs in the picker; it
// just shows no capacity line.
func TestStorageForShareWithUnknownCapacity(t *testing.T) {
	noUsage := func(string, string, string) (uint64, uint64) { return 0, 0 }

	got, err := StorageFor(context.Background(), testConfig("laptop"), "nas", "phil", "hunter2", nil, sharing([]string{"locked"}, nil), noUsage)
	if err != nil {
		t.Fatalf("StorageFor(nas): %v", err)
	}
	want := []Storage{{Kind: "share", Label: "locked", URL: "//nas/locked"}}
	if !slices.Equal(got, want) {
		t.Errorf("storage = %+v, want %+v", got, want)
	}
}

func TestStorageForWithNoSharesOffered(t *testing.T) {
	got, err := StorageFor(context.Background(), testConfig("laptop"), "nas", "phil", "hunter2", nil, sharing(nil, nil), nil)
	if err != nil {
		t.Fatalf("StorageFor(nas): %v", err)
	}
	if len(got) != 0 {
		t.Errorf("storage = %+v, want none", got)
	}
	if got == nil {
		t.Error("storage is nil, want an empty slice")
	}
}

// The same wrapping rule as the live-failure case above, but with an injected
// error: whatever went wrong reaches the caller intact, and the hint is added
// only when no credentials were supplied.
func TestStorageForShareListingErrorIsWrapped(t *testing.T) {
	listErr := errors.New("no route to host")

	cases := []struct {
		name     string
		user     string
		wantHint bool
	}{
		{"no username gets the hint", "", true},
		{"a supplied username gets no hint", "phil", false},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := StorageFor(context.Background(), testConfig("laptop"), "192.168.1.10", c.user, "", nil, sharing(nil, listErr), nil)

			if !errors.Is(err, listErr) {
				t.Fatalf("err = %v, want %v", err, listErr)
			}
			if got != nil {
				t.Errorf("storage = %v, want nil", got)
			}
			if hinted := strings.Contains(err.Error(), "needs a username and password"); hinted != c.wantHint {
				t.Errorf("error %q hinted at credentials = %v, want %v", err, hinted, c.wantHint)
			}
		})
	}
}

// The daemon passes nil for every seam, so nil has to mean the production
// implementation rather than "no drives" or a nil-func panic.
func TestStorageForNilSeamsUseTheRealImplementations(t *testing.T) {
	t.Run("drives", func(t *testing.T) {
		got, err := StorageFor(context.Background(), testConfig("laptop"), KindThis, "", "", nil, nil, nil)
		if err != nil {
			t.Fatalf("StorageFor(%s): %v", KindThis, err)
		}
		if got == nil {
			t.Fatal("storage is nil, want an empty slice")
		}
		// Whatever this runner has mounted, it is local storage.
		for _, s := range got {
			if s.Kind != "drive" {
				t.Errorf("%q: Kind = %q, want %q", s.Label, s.Kind, "drive")
			}
		}
	})

	// A nil usage seam has to reach the real shareUsage, which dials. A refused
	// loopback address makes that fail at once rather than waiting on DNS, and
	// a failed lookup is not a failed listing: the share is still returned.
	t.Run("share capacity", func(t *testing.T) {
		addr := refusedAddr(t)
		got, err := StorageFor(context.Background(), testConfig("laptop"), addr, "phil", "hunter2", nil, sharing([]string{"backups"}, nil), nil)
		if err != nil {
			t.Fatalf("StorageFor(%s): %v", addr, err)
		}
		want := []Storage{{Kind: "share", Label: "backups", URL: "//" + addr + "/backups"}}
		if !slices.Equal(got, want) {
			t.Errorf("storage = %+v, want %+v", got, want)
		}
	})
}
