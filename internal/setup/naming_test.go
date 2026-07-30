// SPDX-License-Identifier: MIT

package setup

import (
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/phil9922/backup-maker/internal/config"
	"github.com/phil9922/backup-maker/internal/localmirror"
)

// THE GUARANTEE: two drives on one server get two names.
//
// The bug this fixes. A box exporting one `backups` share with a directory per
// disk gave every disk the same default name, and adding the second was refused
// outright — "a target named "backups" already exists" — with no suggestion of
// anything that would work. The name was taken from the share, which is a fact
// about the server, not about the storage.
func TestTwoDrivesOnOneServerGetTwoNames(t *testing.T) {
	for _, c := range []struct{ url, want string }{
		{"//pi/backups/drive1", "pi-drive1"},
		{"//pi/backups/drive2", "pi-drive2"},
		{"//pi/backups", "pi-backups"},
		{"//pi.local/backups", "pi-backups"},
		{"//nas.home.arpa/media/photos", "nas-photos"},
		{`\\WINBOX\Backups`, "WINBOX-Backups"},
		{"smb://pi:4450/backups/d1", "pi-d1"},
		// An address says nothing worth reading and stops being true when the
		// lease moves, so it is left out entirely.
		{"//192.168.5.141/backups", "backups"},
		{"//192.168.5.141/backups/drive1", "backups-drive1"},
		// A share name with a space in it is a name nobody typed, so it is
		// tidied rather than refused.
		{"//pi/My Backups", "pi-My-Backups"},
	} {
		if got := DefaultShareTargetName(c.url); got != c.want {
			t.Errorf("%s defaulted to %q, want %q", c.url, got, c.want)
		}
	}
	if got := DefaultShareTargetName("not-a-share"); got != "" {
		t.Errorf("an unparseable address produced the name %q; the caller reports the parse error", got)
	}
}

// THE GUARANTEE: a default that is taken is moved along, not refused.
func TestADefaultNameThatIsTakenIsMovedAlong(t *testing.T) {
	cfg := config.New()
	cfg.Targets = []config.Target{
		{Type: "share", Name: "pi-backups", URL: "//pi/backups", Folders: []string{}},
	}
	if got := FreeTargetName(cfg, "pi-backups"); got != "pi-backups-2" {
		t.Errorf("the second one was called %q, want pi-backups-2", got)
	}
	// Case-insensitively, because "backups" and "Backups" would both work and be
	// impossible to tell apart in any sentence about them.
	if got := FreeTargetName(cfg, "PI-BACKUPS"); got != "PI-BACKUPS-2" {
		t.Errorf("a case-only difference was treated as free: %q", got)
	}
	cfg.Targets = append(cfg.Targets,
		config.Target{Type: "share", Name: "pi-backups-2", URL: "//pi/b2", Folders: []string{}})
	if got := FreeTargetName(cfg, "pi-backups"); got != "pi-backups-3" {
		t.Errorf("the third one was called %q, want pi-backups-3", got)
	}
}

// THE GUARANTEE: a name somebody TYPED is refused rather than quietly changed —
// and the refusal names one that would work.
//
// The two halves are deliberately different. A default nobody chose is ours to
// adjust; a name they asked for is not, because saving something else under it
// is worse than saying no. What the old message got wrong was stopping there.
func TestATypedNameThatCollidesIsRefusedWithOneThatWouldWork(t *testing.T) {
	cfg := config.New()
	cfg.Targets = []config.Target{
		{Type: "drive", Name: "card", Path: "/mnt/card", Folders: []string{}},
	}
	err := CheckNameFree(cfg, "card")
	if err == nil {
		t.Fatal("a name already in use was accepted")
	}
	if !strings.Contains(err.Error(), "card-2") {
		t.Errorf("the refusal does not suggest a name that works: %q", err)
	}
	if err := CheckNameFree(cfg, "CARD"); err == nil {
		t.Error("a case-only difference was accepted; the two would be indistinguishable")
	}
}

// THE GUARANTEE: the decision between "the name you typed" and "the name we
// chose" is made in one place, and it makes both halves right.
//
// Tested here rather than through AddShareTarget, which dials a real server:
// this IS the decision, and every path that creates a destination goes through
// it. A mutation that made a typed name behave like a default survived every
// other test in this file.
func TestATypedNameIsHonouredOrRefused_ADefaultIsMovedAlong(t *testing.T) {
	cfg := config.New()
	cfg.Targets = []config.Target{
		{Type: "share", Name: "pi-backups", URL: "//pi/backups", Folders: []string{}},
	}
	// Nothing typed: ours, so moved along.
	got, err := TargetName(cfg, "", "pi-backups")
	if err != nil {
		t.Fatalf("a default name was refused: %v", err)
	}
	if got != "pi-backups-2" {
		t.Errorf("the default became %q, want pi-backups-2", got)
	}
	// Typed and free: used exactly as typed.
	if got, err := TargetName(cfg, "the shed NAS", "pi-backups"); err != nil || got != "the shed NAS" {
		t.Errorf("a typed name came back as %q (%v), want it untouched", got, err)
	}
	// Typed and taken: refused, never quietly replaced.
	got, err = TargetName(cfg, "pi-backups", "pi-backups")
	if err == nil {
		t.Errorf("a typed name that was taken was silently changed to %q — the user would be looking "+
			"for the name they asked for", got)
	}
	// Typed and unusable: refused before anything is saved.
	if _, err := TargetName(cfg, "with/slash", "pi-backups"); err == nil {
		t.Error("an unusable typed name was accepted")
	}
}

func TestANameThatCannotWorkIsRefusedBeforeAnythingIsSaved(t *testing.T) {
	for _, bad := range []string{"", " leading", "trailing ", "with/slash", "with\\slash",
		"two\nlines", strings.Repeat("x", 65)} {
		if err := ValidTargetName(bad); err == nil {
			t.Errorf("%q was accepted as a destination name", bad)
		}
	}
	for _, ok := range []string{"card", "pi-drive1", "Phil's card", "backup_pi", "카드"} {
		if err := ValidTargetName(ok); err != nil {
			t.Errorf("%q was refused: %v", ok, err)
		}
	}
}

// THE GUARANTEE: renaming a destination moves everything that points at it, and
// leaves nothing behind under the old name.
//
// Every one of these has a failure the user would meet later and not connect to
// a rename: a missed UUID stops backups to that destination with "re-add it", a
// missed password asks for it again, a missed clock reports a drive that has
// been backed up for months as never synced, and a missed archive target fails
// the whole config.
func TestRenamingADestinationLeavesNothingBehindUnderTheOldName(t *testing.T) {
	isolate(t)
	cfg := config.New()
	cfg.General.MachineName = "laptop"
	cfg.Folders = []config.Folder{{ID: "fold1", Path: "/home/p/code", Label: "code"}}
	cfg.Targets = []config.Target{
		{Type: "share", Name: "backups", URL: "//pi/backups", Username: "pk", Folders: []string{}},
		{Type: "drive", Name: "card", Path: "/mnt/card", Folders: []string{}},
	}
	cfg.Archives = []config.Archive{
		{Name: "nightly", Every: "daily", Target: "backups", Keep: 3},
	}
	cfg.Retired = []config.Retired{{
		ID: "gone", Label: "old", Path: "/home/p/old",
		Copies: []config.RetiredCopy{
			{Target: "backups", Type: "share", DestPath: "laptop/old"},
			{Target: "card", Type: "drive", DestPath: "laptop/old"},
		},
	}}
	if err := cfg.Save(); err != nil {
		t.Fatal(err)
	}
	state, err := config.LoadState()
	if err != nil {
		t.Fatal(err)
	}
	synced := time.Date(2026, 7, 20, 9, 0, 0, 0, time.UTC)
	state.DriveTargetUUIDs = map[string]string{"backups": "uuid-pi", "card": "uuid-card"}
	state.ShareCredentials = map[string]string{"backups": "the-password"}
	state.MirrorLastSync = map[string]map[string]time.Time{
		"fold1": {"backups": synced, "card": synced.Add(-time.Hour)},
	}
	state.MirrorScanState = map[string]map[string]config.ScanMark{
		"fold1": {"backups": {TargetUUID: "uuid-pi"}},
	}
	if err := state.Save(); err != nil {
		t.Fatal(err)
	}

	if err := RenameTarget("backups", "pi-drive1"); err != nil {
		t.Fatal(err)
	}

	got, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	if err := got.Validate(); err != nil {
		t.Fatalf("the renamed config no longer validates: %v", err)
	}
	names := []string{}
	for _, tg := range got.Targets {
		names = append(names, tg.Name)
	}
	if strings.Join(names, ",") != "pi-drive1,card" {
		t.Errorf("destinations are now %v, want [pi-drive1 card]", names)
	}
	if got.Archives[0].Target != "pi-drive1" {
		t.Errorf("the snapshot schedule still points at %q", got.Archives[0].Target)
	}
	if got.Retired[0].Copies[0].Target != "pi-drive1" {
		t.Errorf("a stopped folder's copy still names %q", got.Retired[0].Copies[0].Target)
	}
	if got.Retired[0].Copies[1].Target != "card" {
		t.Error("another destination's record was renamed too")
	}

	after, err := config.LoadState()
	if err != nil {
		t.Fatal(err)
	}
	if after.DriveTargetUUIDs["pi-drive1"] != "uuid-pi" {
		t.Error("the recorded identity of the storage did not move: the daemon would say to re-add it")
	}
	if after.ShareCredentials["pi-drive1"] != "the-password" {
		t.Error("the share password did not move: it would be asked for again")
	}
	if !after.MirrorLastSync["fold1"]["pi-drive1"].Equal(synced) {
		t.Error("the last-synced clock did not move: the destination would read as never synced")
	}
	if after.MirrorScanState["fold1"]["pi-drive1"].TargetUUID != "uuid-pi" {
		t.Error("the last-pass record did not move")
	}
	if !after.MirrorLastSync["fold1"]["card"].Equal(synced.Add(-time.Hour)) {
		t.Error("another destination's clock was disturbed")
	}
	for what, present := range map[string]bool{
		"a marker UUID":  after.DriveTargetUUIDs["backups"] != "",
		"a password":     after.ShareCredentials["backups"] != "",
		"a sync clock":   !after.MirrorLastSync["fold1"]["backups"].IsZero(),
		"a scan record":  after.MirrorScanState["fold1"]["backups"].TargetUUID != "",
		"a target entry": strings.Contains(strings.Join(names, ","), "backups"),
	} {
		if present {
			t.Errorf("%s is still filed under the old name", what)
		}
	}
}

// THE GUARANTEE THAT KEEPS THE ONE ABOVE HONEST: a new destination-keyed map in
// State is a rename that silently loses something, so adding one to State fails
// this test until somebody decides.
//
// Reflection over the field COUNT rather than over the keys, because reflection
// cannot tell what a map is keyed by: ArchivePasswords is keyed by archive name
// and moving it would be wrong. So the classification is written down here, and
// the count is what refuses to let a new field arrive unclassified.
func TestEveryMapInStateIsClassifiedAsDestinationKeyedOrNot(t *testing.T) {
	// Keyed by destination name, and RenameTarget must move them.
	byTarget := map[string]bool{
		"DriveTargetUUIDs": true,
		"ShareCredentials": true,
		// Nested: folder id -> target name -> value.
		"MirrorLastSync":  true,
		"MirrorScanState": true,
	}
	// Keyed by something else, and RenameTarget must NOT touch them.
	byOther := map[string]bool{
		"LANDevices":       true, // the token that view issued
		"ArchivePasswords": true, // archive job name
		"ArchiveLastRun":   true, // archive job name
	}

	st := reflect.TypeOf(config.State{})
	var unclassified []string
	for i := range st.NumField() {
		f := st.Field(i)
		if f.Type.Kind() != reflect.Map {
			continue
		}
		if !byTarget[f.Name] && !byOther[f.Name] {
			unclassified = append(unclassified, f.Name)
		}
	}
	if len(unclassified) > 0 {
		t.Errorf("config.State has map fields this test does not classify: %v.\n"+
			"Decide whether each is keyed by a DESTINATION name. If it is, teach "+
			"setup.RenameTarget to move it and add it to byTarget here — a missed one "+
			"means a rename silently loses that destination's identity, password or "+
			"sync clocks. If it is not, add it to byOther.", unclassified)
	}
}

func TestRenamingRefusesWhatWouldBreakTheConfig(t *testing.T) {
	isolate(t)
	cfg := config.New()
	cfg.Targets = []config.Target{
		{Type: "drive", Name: "card", Path: "/mnt/card", Folders: []string{}},
		{Type: "drive", Name: "usb", Path: "/mnt/usb", Folders: []string{}},
	}
	if err := cfg.Save(); err != nil {
		t.Fatal(err)
	}
	err := RenameTarget("card", "usb")
	if err == nil {
		t.Fatal("renaming one destination onto another's name was allowed: they would share a UUID and a password")
	}
	// The same message an add gets. Two different refusals for one situation is
	// how one of them stays a dead end — which is the fault being fixed here.
	if !strings.Contains(err.Error(), "usb-2") {
		t.Errorf("renaming into a taken name does not suggest one that works: %v", err)
	}
	if err := RenameTarget("card", "USB"); err == nil {
		t.Error("a case-only collision was allowed")
	}
	if err := RenameTarget("card", "bad/name"); err == nil {
		t.Error("an unusable name was accepted")
	}
	if err := RenameTarget("nothing-here", "fine"); err == nil {
		t.Error("renaming a destination that does not exist was reported as success")
	}
	got, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	if got.Targets[0].Name != "card" || got.Targets[1].Name != "usb" {
		t.Errorf("a refused rename changed the config anyway: %q, %q",
			got.Targets[0].Name, got.Targets[1].Name)
	}
}

// THE GUARANTEE: describing a drive does not change what the drive IS.
//
// The marker file is what says the storage at a mount point is still the storage
// this destination was set up against. Writing a fresh marker to add a sentence
// would replace that UUID — and the destination would be refused as foreign
// storage the moment the description was saved, which is every backup to it
// stopping because somebody typed a note.
func TestDescribingADestinationKeepsItsIdentity(t *testing.T) {
	isolate(t)
	root := t.TempDir()
	if err := localmirror.WriteMarkerAt(root, "uuid-card", "laptop"); err != nil {
		t.Fatal(err)
	}
	cfg := config.New()
	cfg.General.MachineName = "laptop"
	cfg.Targets = []config.Target{{Type: "drive", Name: "card", Path: root, Folders: []string{}}}
	if err := cfg.Save(); err != nil {
		t.Fatal(err)
	}

	if err := DescribeTarget("card", "Samsung 128GB card, lives in the laptop"); err != nil {
		t.Fatal(err)
	}
	m, err := localmirror.ReadMarkerAt(root)
	if err != nil {
		t.Fatal(err)
	}
	if m.TargetUUID != "uuid-card" {
		t.Errorf("the storage's identity changed to %q: it would now be refused as a foreign drive", m.TargetUUID)
	}
	if m.MachineName != "laptop" {
		t.Errorf("the marker's machine name changed to %q", m.MachineName)
	}
	if m.Description != "Samsung 128GB card, lives in the laptop" {
		t.Errorf("the description was not written: %q", m.Description)
	}

	// Cleared without disturbing the identity either.
	if err := DescribeTarget("card", ""); err != nil {
		t.Fatal(err)
	}
	m, err = localmirror.ReadMarkerAt(root)
	if err != nil {
		t.Fatal(err)
	}
	if m.Description != "" {
		t.Errorf("the description was not cleared: %q", m.Description)
	}
	if m.TargetUUID != "uuid-card" {
		t.Error("clearing the description changed the storage's identity")
	}
}

// A destination that is not there cannot be described, and says so — rather than
// appearing to save and losing the sentence.
func TestDescribingADestinationThatIsNotThereFails(t *testing.T) {
	isolate(t)
	cfg := config.New()
	cfg.Targets = []config.Target{
		{Type: "drive", Name: "card", Path: "/definitely/not/mounted", Folders: []string{}},
		{Type: "device", Name: "laptop2", DeviceID: "AAAA-BBBB", Folders: []string{}},
	}
	if err := cfg.Save(); err != nil {
		t.Fatal(err)
	}
	if err := DescribeTarget("card", "a note"); err == nil {
		t.Error("describing an unreachable destination reported success")
	}
	// The message has to say WHICH refusal this is: "there is nothing there" and
	// "that is a computer, not a drive of ours" are different problems, and a
	// test that accepts either cannot tell whether the guard is present.
	err := DescribeTarget("laptop2", "a note")
	if err == nil {
		t.Fatal("a paired computer accepted a description of a drive we do not own")
	}
	if !strings.Contains(err.Error(), "paired computer") {
		t.Errorf("describing a paired computer failed for the wrong reason: %v", err)
	}
	if err := DescribeTarget("card", strings.Repeat("x", 201)); err == nil {
		t.Error("a description too long to show anywhere was accepted")
	}
}
