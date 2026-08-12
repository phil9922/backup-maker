// SPDX-License-Identifier: MIT

package setup

import (
	"path/filepath"
	"testing"
)

// Somebody who keeps their work in three directories protects all three in one
// pass of the wizard, not three passes with three chances to stop half way.
func TestSeveralFoldersInOneRequestAreAllProtected(t *testing.T) {
	isolate(t)
	base := t.TempDir()
	code := mustDir(t, base, "code")
	docs := mustDir(t, base, "documents")
	photos := mustDir(t, base, "photos")
	dest := mustDir(t, base, "card")

	if _, _, err := CreateBackup(BackupRequest{
		Folders:      []FolderRef{{Path: code}, {Path: docs}, {Path: photos}},
		Destinations: []Destination{{Path: dest}},
	}); err != nil {
		t.Fatal(err)
	}

	cfg := load(t)
	if len(cfg.Folders) != 3 {
		t.Fatalf("got %d folders saved, want the 3 that were chosen: %+v", len(cfg.Folders), cfg.Folders)
	}
	for _, want := range []string{code, docs, photos} {
		found := false
		for _, f := range cfg.Folders {
			if f.Path != want {
				continue
			}
			found = true
			if !cfg.Mirrored(f.ID) {
				t.Errorf("%s was saved but nothing backs it up", want)
			}
		}
		if !found {
			t.Errorf("%s was chosen and never saved", want)
		}
	}
}

// THE GUARANTEE, and the rule this project has broken three times: an empty
// Folders list on a destination means EVERY folder on the machine. A run of the
// wizard that creates a destination must scope it to exactly the folders that
// were chosen.
func TestSettingUpSeveralFoldersNeverLeavesADestinationScopedToEverything(t *testing.T) {
	isolate(t)
	base := t.TempDir()
	code := mustDir(t, base, "code")
	docs := mustDir(t, base, "documents")
	unrelated := mustDir(t, base, "unrelated")
	dest := mustDir(t, base, "card")

	// A folder nobody asked to send to the new destination. If the destination
	// ends up meaning "every folder" this is what quietly starts being copied.
	if _, err := AddFolder(unrelated, "", nil, false); err != nil {
		t.Fatal(err)
	}

	_, targets, err := CreateBackup(BackupRequest{
		Folders:      []FolderRef{{Path: code}, {Path: docs}},
		Destinations: []Destination{{Path: dest}},
	})
	if err != nil {
		t.Fatal(err)
	}

	cfg := load(t)
	target := loadTarget(t, targets[0].Name)
	if len(target.Folders) != 2 {
		t.Fatalf("destination %q is scoped to %v; an empty or short list means folders nobody chose", target.Name, target.Folders)
	}
	for _, f := range cfg.FoldersForTarget(target) {
		if f.Path == unrelated {
			t.Fatalf("destination %q started backing up %q, which was never part of the request", target.Name, f.Label)
		}
	}
	// And the two that WERE chosen are both there — a scoped list that lost one
	// is the same failure in the other direction.
	for _, want := range []string{code, docs} {
		mirrored := false
		for _, f := range cfg.FoldersForTarget(target) {
			if f.Path == want {
				mirrored = true
			}
		}
		if !mirrored {
			t.Errorf("%s was chosen but destination %q does not cover it", want, target.Name)
		}
	}
}

// A destination already scoped to one folder gains the new ones and keeps the
// old: the ids already there are folders somebody asked this destination to
// hold, and this request said nothing about them.
func TestAddingFoldersExtendsAnAlreadyScopedDestinationRatherThanReplacingIt(t *testing.T) {
	isolate(t)
	base := t.TempDir()
	first := mustDir(t, base, "first")
	code := mustDir(t, base, "code")
	docs := mustDir(t, base, "documents")
	dest := mustDir(t, base, "card")

	existing, targets, err := CreateBackup(BackupRequest{
		Path: first, Destinations: []Destination{{Path: dest}},
	})
	if err != nil {
		t.Fatal(err)
	}
	name := targets[0].Name

	if _, _, err := CreateBackup(BackupRequest{
		Folders:      []FolderRef{{Path: code}, {Path: docs}},
		Destinations: []Destination{{ExistingTarget: name}},
	}); err != nil {
		t.Fatal(err)
	}

	cfg := load(t)
	target := loadTarget(t, name)
	if len(target.Folders) != 3 || target.Folders[0] != existing.ID {
		t.Fatalf("destination %q is scoped to %v; adding folders must extend the list, not replace it", name, target.Folders)
	}
	if !cfg.Mirrored(existing.ID) {
		t.Error("the folder that was already going to this destination stopped being backed up")
	}
}

// The other half of the same rule: a destination that already means "every
// folder" is left exactly as it is. Narrowing it to the selection would stop
// every other folder on the machine from being copied there.
func TestAddingFoldersLeavesAnEveryFolderDestinationUnscoped(t *testing.T) {
	isolate(t)
	base := t.TempDir()
	code := mustDir(t, base, "code")
	docs := mustDir(t, base, "documents")
	dest := mustDir(t, base, "card")

	if _, err := AddDriveTarget(dest, "catchall"); err != nil {
		t.Fatal(err)
	}
	if len(loadTarget(t, "catchall").Folders) != 0 {
		t.Fatal("precondition: this test needs the every-folder case")
	}

	if _, _, err := CreateBackup(BackupRequest{
		Folders:      []FolderRef{{Path: code}, {Path: docs}},
		Destinations: []Destination{{ExistingTarget: "catchall"}},
	}); err != nil {
		t.Fatal(err)
	}

	if got := loadTarget(t, "catchall").Folders; len(got) != 0 {
		t.Errorf("an every-folder destination was narrowed to %v, so everything else it held stops being copied", got)
	}
}

// One schedule for the whole selection, naming every folder it covers. Two
// schedules would race each other onto the same destination, and a schedule with
// no folder list would mean EVERY folder.
func TestOneTimedRequestMakesOneScheduleNamingEverySelectedFolder(t *testing.T) {
	isolate(t)
	base := t.TempDir()
	code := mustDir(t, base, "code")
	docs := mustDir(t, base, "documents")
	unrelated := mustDir(t, base, "unrelated")
	dest := mustDir(t, base, "card")

	if _, err := AddFolder(unrelated, "", nil, false); err != nil {
		t.Fatal(err)
	}

	if _, _, err := CreateBackup(BackupRequest{
		Folders:      []FolderRef{{Path: code}, {Path: docs}},
		Mode:         ModeTimed,
		Destinations: []Destination{{Path: dest}},
		Archive:      &ArchiveSpec{Name: "nightly", Every: "daily", Keep: 3, Password: "pw"},
	}); err != nil {
		t.Fatal(err)
	}

	cfg := load(t)
	if len(cfg.Archives) != 1 {
		t.Fatalf("got %d schedules, want exactly one covering both folders: %+v", len(cfg.Archives), cfg.Archives)
	}
	a := cfg.Archives[0]
	if len(a.Folders) != 2 {
		t.Fatalf("schedule %q names %v; an empty or partial list is every folder or the wrong ones", a.Name, a.Folders)
	}
	covered := map[string]bool{}
	for _, f := range cfg.FoldersForArchive(a) {
		covered[f.Path] = true
	}
	if !covered[code] || !covered[docs] {
		t.Errorf("schedule %q covers %v, want both chosen folders", a.Name, covered)
	}
	if covered[unrelated] {
		t.Errorf("schedule %q swept in %q, which nobody chose", a.Name, unrelated)
	}
}

// Every folder in a timed request is snapshot-only, on the folder itself. Miss
// one and every destination that mirrors "every folder" starts a continuous copy
// of it — which is what happened on 2026-07-28.
func TestEveryFolderInATimedRequestIsMarkedSnapshotOnly(t *testing.T) {
	isolate(t)
	base := t.TempDir()
	code := mustDir(t, base, "code")
	docs := mustDir(t, base, "documents")
	catchall := mustDir(t, base, "catchall")
	dest := mustDir(t, base, "card")

	// The destination that predates per-folder scoping, and the reason the flag
	// has to be on the folder rather than only on the new destination.
	if _, err := AddDriveTarget(catchall, "catchall"); err != nil {
		t.Fatal(err)
	}

	if _, _, err := CreateBackup(BackupRequest{
		Folders:      []FolderRef{{Path: code}, {Path: docs}},
		Mode:         ModeTimed,
		Destinations: []Destination{{Path: dest}},
		Archive:      &ArchiveSpec{Name: "nightly", Every: "daily", Keep: 3, Password: "pw"},
	}); err != nil {
		t.Fatal(err)
	}

	cfg := load(t)
	for _, f := range cfg.Folders {
		if !f.SnapshotOnly {
			t.Errorf("%q was asked for as a scheduled snapshot and is not marked snapshot-only", f.Label)
		}
		if cfg.Mirrored(f.ID) {
			t.Errorf("%q is being mirrored continuously; only scheduled snapshots were asked for", f.Label)
		}
	}
}

// All or nothing across the selection: a folder that fails validation must take
// the whole request down with it. Half-applying would tell somebody all three
// folders are safe while only the first one is.
func TestAFailureOnTheSecondFolderLeavesTheFirstUnsaved(t *testing.T) {
	isolate(t)
	base := t.TempDir()
	code := mustDir(t, base, "code")
	docs := mustDir(t, base, "documents")
	dest := mustDir(t, base, "card")

	_, _, err := CreateBackup(BackupRequest{
		Folders: []FolderRef{
			{Path: code},
			{Path: filepath.Join(base, "was-unplugged")},
			{Path: docs},
		},
		Destinations: []Destination{{Path: dest}},
	})
	if err == nil {
		t.Fatal("a folder that does not exist was accepted")
	}

	cfg := load(t)
	if len(cfg.Folders) != 0 {
		t.Errorf("the request failed and still saved %+v", cfg.Folders)
	}
	if len(cfg.Targets) != 0 {
		t.Errorf("the request failed and still saved destination %+v", cfg.Targets)
	}
}

// The same, one step later: everything about the folders is fine and the
// destination is not.
func TestAFailedDestinationLeavesEveryChosenFolderUnsaved(t *testing.T) {
	isolate(t)
	base := t.TempDir()
	code := mustDir(t, base, "code")
	docs := mustDir(t, base, "documents")
	good := mustDir(t, base, "card")

	if _, _, err := CreateBackup(BackupRequest{
		Folders: []FolderRef{{Path: code}, {Path: docs}},
		Destinations: []Destination{
			{Path: good},
			{Path: filepath.Join(base, "not-plugged-in")},
		},
	}); err == nil {
		t.Fatal("a destination that is not there was accepted")
	}

	cfg := load(t)
	if len(cfg.Folders) != 0 || len(cfg.Targets) != 0 {
		t.Errorf("a rejected request wrote folders %+v and targets %+v", cfg.Folders, cfg.Targets)
	}
}

// The older form of the request is still the whole contract for callers that
// only ever protect one folder.
func TestTheSingleFolderRequestStillWorksUnchanged(t *testing.T) {
	isolate(t)
	base := t.TempDir()
	src := mustDir(t, base, "code")
	dest := mustDir(t, base, "card")

	folder, targets, err := CreateBackup(BackupRequest{
		Path: src, Label: "code", Destinations: []Destination{{Path: dest}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if folder.Path != src || folder.Label != "code" {
		t.Fatalf("got folder %+v, want the one that was asked for", folder)
	}

	cfg := load(t)
	if len(cfg.Folders) != 1 {
		t.Fatalf("got %+v, want exactly the one folder", cfg.Folders)
	}
	target := loadTarget(t, targets[0].Name)
	if len(target.Folders) != 1 || target.Folders[0] != folder.ID {
		t.Errorf("destination scoped to %v, want just [%s]", target.Folders, folder.ID)
	}

	// And the second kind of backup for a folder that already exists, which is
	// the other half of the old form.
	pi := mustDir(t, base, "pi")
	same, _, err := CreateBackup(BackupRequest{
		FolderID: folder.ID, Mode: ModeTimed,
		Destinations: []Destination{{Path: pi}},
		Archive:      &ArchiveSpec{Every: "daily", Password: "pw", Name: "daily-code"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if same.ID != folder.ID {
		t.Errorf("got folder %s, want the existing %s", same.ID, folder.ID)
	}
}

// A folder listed twice is a selection the caller did not mean. It is REFUSED
// rather than de-duplicated: the two entries can disagree about the label and
// the excludes, and silently honouring one of them is a backup nobody asked for.
func TestChoosingTheSameFolderTwiceIsRefused(t *testing.T) {
	isolate(t)
	base := t.TempDir()
	code := mustDir(t, base, "code")
	docs := mustDir(t, base, "documents")
	dest := mustDir(t, base, "card")

	if _, _, err := CreateBackup(BackupRequest{
		Folders:      []FolderRef{{Path: code}, {Path: docs}, {Path: code}},
		Destinations: []Destination{{Path: dest}},
	}); err == nil {
		t.Error("the same path was accepted twice in one request")
	}
	if cfg := load(t); len(cfg.Folders) != 0 {
		t.Errorf("the refused request still saved %+v", cfg.Folders)
	}

	// And the same folder named once by id and once by path, which is what a
	// wizard that offers both a picker and the "already protected" list can
	// produce.
	existing, _, err := CreateBackup(BackupRequest{
		Path: code, Destinations: []Destination{{Path: dest}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := CreateBackup(BackupRequest{
		Folders:      []FolderRef{{FolderID: existing.ID}, {Path: code}},
		Destinations: []Destination{{ExistingTarget: "card"}},
	}); err == nil {
		t.Error("one folder named by both its id and its path was accepted as two")
	}
	if _, _, err := CreateBackup(BackupRequest{
		Folders:      []FolderRef{{FolderID: existing.ID}, {FolderID: existing.ID}},
		Destinations: []Destination{{ExistingTarget: "card"}},
	}); err == nil {
		t.Error("the same folder id was accepted twice in one request")
	}
}

// Nothing chosen is a mistake with a clear answer, not an empty request that
// quietly succeeds — and emphatically not a destination scoped to nothing,
// which would mean every folder.
func TestARequestWithNoFoldersIsRefused(t *testing.T) {
	isolate(t)
	dest := mustDir(t, t.TempDir(), "card")

	for _, req := range []BackupRequest{
		{Destinations: []Destination{{Path: dest}}},
		{Folders: []FolderRef{{}}, Destinations: []Destination{{Path: dest}}},
		{Folders: []FolderRef{{Path: "  "}}, Destinations: []Destination{{Path: dest}}},
	} {
		if _, _, err := CreateBackup(req); err == nil {
			t.Errorf("a request naming no folder was accepted: %+v", req)
		}
	}
	if cfg := load(t); len(cfg.Targets) != 0 {
		t.Errorf("a request naming no folder still created destination %+v", cfg.Targets)
	}
}

// A paired computer takes the whole selection too, and is scoped to it: the
// device target is built by hand rather than through scopeFor, so it is its own
// chance to leave a target meaning "every folder".
func TestSeveralFoldersSentToAPairedComputerAreScopedToThatSelection(t *testing.T) {
	isolate(t)
	base := t.TempDir()
	code := mustDir(t, base, "code")
	docs := mustDir(t, base, "documents")

	if _, _, err := CreateBackup(BackupRequest{
		Folders:      []FolderRef{{Path: code}, {Path: docs}},
		Destinations: []Destination{{DeviceID: goodDeviceID, Name: "attic-pi"}},
	}); err != nil {
		t.Fatal(err)
	}

	cfg := load(t)
	target := loadTarget(t, "attic-pi")
	if len(target.Folders) != 2 {
		t.Fatalf("the paired machine is scoped to %v, want exactly the two folders chosen", target.Folders)
	}
	for _, f := range cfg.Folders {
		if !cfg.Mirrored(f.ID) {
			t.Errorf("%q was chosen and is being sent nowhere", f.Label)
		}
	}
}
