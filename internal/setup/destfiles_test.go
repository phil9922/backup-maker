// SPDX-License-Identifier: MIT

package setup

import (
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/phil9922/backup-maker/internal/archive"
	"github.com/phil9922/backup-maker/internal/config"
	"github.com/phil9922/backup-maker/internal/localmirror"
	"github.com/phil9922/backup-maker/internal/statuspage"
	"github.com/phil9922/backup-maker/internal/testpath"
)

// fileViewDrive builds a destination that looks like a real one after a year of
// use, and the configuration that goes with it:
//
//	my-laptop/                          this computer's backups (claimed, with a manifest)
//	  Development/notes.txt             a folder that is still backed up
//	  photos/holiday.jpg                a folder that was stopped, copy kept
//	  .backup-maker-manifest.json       bookkeeping
//	  .backup-maker-machine.json        the claim
//	  backup-maker-status.html          the status page
//	  .backup-maker-manual/index.html   the manual
//	.backup-maker-versions/my-laptop/Development/
//	  notes~…txt                        a superseded version: notes.txt is still live
//	  gone~…txt                         the LAST copy of a file deleted from the source
//	backup-maker-archives/my-laptop/
//	  nightly/…zip                      a schedule that still exists
//	  pk-probook-desktop/…zip           a schedule that was deleted: 9.9GB of orphans
//	attic-pi/                           another computer's backups, on the same drive
//	lost+found/                         the drive's own business, not backup-maker's
//	somebody-elses-folder/              a directory with no manifest
//	backup-maker-status.html            the index page at the root
//	.backup-maker-target.json           the marker
func fileViewDrive(t *testing.T) string {
	t.Helper()
	isolate(t)
	root := t.TempDir()

	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	cfg.General.MachineName = "my-laptop"
	cfg.Folders = []config.Folder{{ID: "f1", Path: testpath.Abs("/home/alex/Development"), Label: "Development"}}
	cfg.Targets = []config.Target{{Type: "drive", Name: "laptopcard", Path: root}}
	cfg.Archives = []config.Archive{{Name: "nightly", Folders: []string{"f1"}, Every: "daily", Target: "laptopcard", Keep: 3}}
	cfg.Retired = []config.Retired{{
		ID: "f2", Path: testpath.Abs("/home/alex/Photos"), Label: "photos",
		MachineName: "my-laptop",
		StoppedAt:   time.Date(2026, 6, 1, 9, 0, 0, 0, time.UTC),
		Copies: []config.RetiredCopy{{
			Target: "laptopcard", Type: "drive",
			DestPath:     config.DestRoot("my-laptop", "photos"),
			VersionsPath: config.VersionRoot("my-laptop", "photos"),
		}},
	}}
	if err := cfg.Save(); err != nil {
		t.Fatal(err)
	}
	// Writes the marker, claims my-laptop/ for this installation, and records
	// the UUID the recognition gate checks.
	if err := EnsureTargetMarker(localmirror.NewLocalFS(root), "laptopcard", "my-laptop"); err != nil {
		t.Fatal(err)
	}

	writeUnder(t, root, "my-laptop/"+ManifestName, `{"version":2,"machine_name":"my-laptop"}`)
	writeUnder(t, root, "my-laptop/Development/notes.txt", "live copy")
	writeUnder(t, root, "my-laptop/photos/holiday.jpg", "a stopped folder's copy")
	writeUnder(t, root, "my-laptop/"+statuspage.FileName, "<html>status</html>")
	writeUnder(t, root, "my-laptop/.backup-maker-manual/index.html", "<html>manual</html>")

	writeUnder(t, root, config.VersionRoot("my-laptop", "Development")+"/notes~20260101-000000.txt", "an older notes.txt")
	writeUnder(t, root, config.VersionRoot("my-laptop", "Development")+"/gone~20260101-000000.txt", "the only copy left")

	writeUnder(t, root, archive.PathFor("my-laptop", "nightly")+"/nightly-20260801-020000.zip", "encrypted")
	writeUnder(t, root, archive.PathFor("my-laptop", "pk-probook-desktop")+"/pk-probook-desktop-20260101-020000.zip", "orphaned")

	writeUnder(t, root, "attic-pi/"+ManifestName, `{"version":2,"machine_name":"attic-pi"}`)
	writeUnder(t, root, "attic-pi/photos/theirs.jpg", "another computer's backup")

	writeUnder(t, root, "lost+found/#131073", "the filesystem's own business")
	writeUnder(t, root, "somebody-elses-folder/tax-return.pdf", "not a backup at all")
	writeUnder(t, root, statuspage.FileName, "<html>index</html>")
	return root
}

// names is what the view actually showed, in the order it showed it.
func names(entries []DestEntry) []string {
	out := make([]string, len(entries))
	for i, e := range entries {
		out[i] = e.Name
	}
	return out
}

func entryNamed(t *testing.T, l DestListing, name string) DestEntry {
	t.Helper()
	for _, e := range l.Entries {
		if e.Name == name {
			return e
		}
	}
	t.Fatalf("%q was not listed under %q; got %v", name, l.Path, names(l.Entries))
	return DestEntry{}
}

// THE CONTAINMENT RULE. This is a view of backups, not a file manager for the
// drive: the top level is synthesised from the three places backup-maker
// writes, and a directory that is not one of them is not shown — whatever else
// is on the storage.
func TestBrowsingADestinationOnlyEverListsTheBackupRoots(t *testing.T) {
	root := fileViewDrive(t)

	top, err := ListDestFiles("laptopcard", "", opener(root))
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	for _, n := range names(top.Entries) {
		got[n] = true
	}
	for _, want := range []string{"my-laptop", "attic-pi", config.VersionsDirName, archive.DirName} {
		if !got[want] {
			t.Errorf("the top level did not show %q; got %v", want, names(top.Entries))
		}
	}
	for _, never := range []string{
		"lost+found",            // the drive's own business
		"somebody-elses-folder", // a directory with no manifest is not a machine's backups
		statuspage.FileName,     // the index page at the root
		localmirror.MarkerName,  // the marker that says this is our storage
		localmirror.ClaimName,   // (not at the root, but never a row either)
		".backup-maker-manual",  // the copy of the manual
	} {
		if got[never] {
			t.Errorf("the top level listed %q, which is not a backup", never)
		}
	}
	if top.Parent != nil {
		t.Errorf("the top level has a parent (%q); there is nothing above it", *top.Parent)
	}

	// And inside this computer's directory: the folders, and none of the
	// bookkeeping backup-maker keeps beside them.
	mine, err := ListDestFiles("laptopcard", "my-laptop", opener(root))
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"Development", "photos"}; strings.Join(names(mine.Entries), ",") != strings.Join(want, ",") {
		t.Errorf("listing my-laptop showed %v, want %v", names(mine.Entries), want)
	}
	if mine.Parent == nil || *mine.Parent != "" {
		t.Errorf("my-laptop's parent should be the top level, got %v", mine.Parent)
	}
}

// The view answers requests for paths, so anything it is willing to resolve is
// somewhere a delete could be aimed. A path that is not inside one of the
// backup roots is refused rather than cleaned up and used — the same rule
// TestDeletingRetiredBackupsRefusesAHandEditedPath states for stored paths.
func TestListingRefusesAPathOutsideTheBackupRoots(t *testing.T) {
	root := fileViewDrive(t)

	for _, bad := range []struct{ name, path string }{
		{"parent traversal", ".."},
		{"traversal through a real root", "my-laptop/../.."},
		{"absolute", testpath.Abs("/etc")},
		{"windows absolute", `C:\Windows`},
		{"backslashes", `my-laptop\Development`},
		{"unclean", "./my-laptop"},
		{"double slash", "my-laptop//Development"},
		{"a sibling directory on the drive", "lost+found"},
		{"a directory with no manifest", "somebody-elses-folder"},
		{"a file at the destination root", statuspage.FileName},
		{"the marker", localmirror.MarkerName},
		{"this directory", "."},
	} {
		t.Run(bad.name, func(t *testing.T) {
			if _, err := ListDestFiles("laptopcard", bad.path, opener(root)); err == nil {
				t.Errorf("listed %q, which is not somewhere backup-maker keeps backups", bad.path)
			}
			// And the same path cannot be deleted either, whatever is typed.
			if _, err := DeleteDestFile("laptopcard", bad.path, filepath.Base(bad.path), opener(root)); err == nil {
				t.Errorf("deleted %q, which is not somewhere backup-maker keeps backups", bad.path)
			}
		})
	}
	if !exists(t, root, "somebody-elses-folder/tax-return.pdf") {
		t.Fatal("a file outside the backup roots was deleted")
	}
	if !exists(t, root, "lost+found/#131073") {
		t.Fatal("a file outside the backup roots was deleted")
	}
}

// A root, or a whole computer's directory inside one, would take out far more
// than the person pointed at. The minimum depth per root is what refuses it —
// see loc.minSegments.
func TestDeletingFromTheFileViewRefusesARootItself(t *testing.T) {
	root := fileViewDrive(t)

	for _, whole := range []string{
		"my-laptop",                           // every folder this computer backs up here
		config.VersionsDirName,                // every version of every file
		archive.DirName,                       // every snapshot on the drive
		config.VersionsDirName + "/my-laptop", // one computer's whole history
		archive.DirName + "/my-laptop",        // every snapshot this computer wrote
	} {
		if _, err := DeleteDestFile("laptopcard", whole, filepath.Base(whole), opener(root)); err == nil {
			t.Errorf("deleted %q — a whole store, not one backup", whole)
		}
		if !exists(t, root, whole) {
			t.Fatalf("%q was removed", whole)
		}
	}
	// The listing says the same thing, so no button is ever offered for them.
	top, err := ListDestFiles("laptopcard", "", opener(root))
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range top.Entries {
		if e.Deletable {
			t.Errorf("the top level offered to delete %q", e.Name)
		}
	}
}

// Deleting something a live task owns would remove a working backup. It is
// refused, in a sentence that names the task — the same spirit as
// Config.DeleteBlockedReason.
func TestDeletingFromTheFileViewRefusesSomethingStillInUse(t *testing.T) {
	root := fileViewDrive(t)

	for _, c := range []struct{ path, names string }{
		{"my-laptop/Development", "Development"},
		{"my-laptop/Development/notes.txt", "Development"},
		{config.VersionRoot("my-laptop", "Development"), "Development"},
		{archive.PathFor("my-laptop", "nightly"), "nightly"},
		{archive.PathFor("my-laptop", "nightly") + "/nightly-20260801-020000.zip", "nightly"},
	} {
		_, err := DeleteDestFile("laptopcard", c.path, filepath.Base(c.path), opener(root))
		if err == nil {
			t.Fatalf("deleted %q, which a live backup is still maintaining", c.path)
		}
		if !strings.Contains(err.Error(), c.names) {
			t.Errorf("the refusal for %q does not name the task that owns it: %v", c.path, err)
		}
		if !exists(t, root, c.path) {
			t.Errorf("%q was deleted despite being in use", c.path)
		}
	}
}

// The typed name is enforced here, not in the browser. Every file survives a
// wrong one.
func TestDeletingFromTheFileViewRefusesWithoutTheTypedName(t *testing.T) {
	root := fileViewDrive(t)
	orphan := archive.PathFor("my-laptop", "pk-probook-desktop")

	for _, wrong := range []string{"", "  ", "pk-probook", "PK-PROBOOK-DESKTOP", "backup-maker-archives"} {
		if _, err := DeleteDestFile("laptopcard", orphan, wrong, opener(root)); err == nil {
			t.Errorf("a delete went ahead with confirmation %q", wrong)
		}
		if !exists(t, root, orphan) {
			t.Fatalf("files were deleted despite the confirmation %q not matching", wrong)
		}
	}
}

// Foreign storage at a familiar mount point is how a recursive delete finds
// somebody else's files. The marker must match before anything is listed or
// removed.
func TestDeletingFromTheFileViewRefusesOnUnrecognisedStorage(t *testing.T) {
	root := fileViewDrive(t)
	// Someone else's disk, mounted where ours used to be, with a directory of
	// the same shape on it.
	stranger := t.TempDir()
	writeUnder(t, stranger, "my-laptop/"+ManifestName, `{"version":2,"machine_name":"my-laptop"}`)
	writeUnder(t, stranger, archive.PathFor("my-laptop", "pk-probook-desktop")+"/theirs.zip", "a stranger's file")

	if _, err := DeleteDestFile("laptopcard", archive.PathFor("my-laptop", "pk-probook-desktop"),
		"pk-probook-desktop", opener(stranger)); err == nil {
		t.Fatal("deleted from storage that is not the destination backup-maker knows")
	}
	if !exists(t, stranger, archive.PathFor("my-laptop", "pk-probook-desktop")+"/theirs.zip") {
		t.Error("a stranger's files were deleted")
	}
	// Not even listed: enumerating a stranger's disk is not something to do on
	// the strength of a mount point either.
	if _, err := ListDestFiles("laptopcard", "", opener(stranger)); err == nil {
		t.Error("listed storage that is not the destination backup-maker knows")
	}
	// The real drive is untouched by all of this.
	if !exists(t, root, archive.PathFor("my-laptop", "pk-probook-desktop")) {
		t.Error("the real destination lost files")
	}
}

// THE CASE THIS FEATURE EXISTS FOR: a schedule was deleted months ago and its
// zips are still sitting there, named by nothing in the configuration. They are
// shown as leftovers, they can be deleted, and nothing else goes with them.
func TestALeftoverSnapshotJobCanBeDeleted(t *testing.T) {
	root := fileViewDrive(t)
	orphan := archive.PathFor("my-laptop", "pk-probook-desktop")

	jobs, err := ListDestFiles("laptopcard", archive.DirName+"/my-laptop", opener(root))
	if err != nil {
		t.Fatal(err)
	}
	left := entryNamed(t, jobs, "pk-probook-desktop")
	if left.Owner != OwnerLeftover || !left.Deletable {
		t.Errorf("a deleted schedule's snapshots are %q/deletable=%v, want leftover and deletable (%s)",
			left.Owner, left.Deletable, left.Why)
	}
	if left.Kind != KindJob {
		t.Errorf("a snapshot job's directory is kind %q, want %q", left.Kind, KindJob)
	}
	live := entryNamed(t, jobs, "nightly")
	if live.Owner != OwnerInUse || live.Deletable {
		t.Errorf("a live schedule's snapshots are %q/deletable=%v, want in-use and not deletable", live.Owner, live.Deletable)
	}

	res, err := DeleteDestFile("laptopcard", orphan, "pk-probook-desktop", opener(root))
	if err != nil {
		t.Fatalf("deleting a deleted schedule's snapshots: %v", err)
	}
	if res.Owner != OwnerLeftover || res.Path != orphan {
		t.Errorf("reported %+v", res)
	}
	if exists(t, root, orphan) {
		t.Error("the orphaned snapshots are still there")
	}
	// And nothing else went with them.
	for _, keep := range []string{
		archive.PathFor("my-laptop", "nightly") + "/nightly-20260801-020000.zip",
		"my-laptop/Development/notes.txt",
		"my-laptop/photos/holiday.jpg",
		"attic-pi/photos/theirs.jpg",
		config.VersionRoot("my-laptop", "Development") + "/notes~20260101-000000.txt",
	} {
		if !exists(t, root, keep) {
			t.Errorf("deleting one schedule's snapshots also removed %s", keep)
		}
	}
}

// A folder that was stopped is named as stopped, and its copy — the whole
// reason "No longer protected" exists — can be removed from here too.
func TestAStoppedFoldersCopyIsNamedAndCanBeDeleted(t *testing.T) {
	root := fileViewDrive(t)

	mine, err := ListDestFiles("laptopcard", "my-laptop", opener(root))
	if err != nil {
		t.Fatal(err)
	}
	stopped := entryNamed(t, mine, "photos")
	if stopped.Owner != OwnerStopped || !stopped.Deletable {
		t.Errorf("a stopped folder's copy is %q/deletable=%v, want stopped and deletable (%s)",
			stopped.Owner, stopped.Deletable, stopped.Why)
	}
	if !strings.Contains(stopped.Why, "photos") {
		t.Errorf("the explanation does not name the folder: %q", stopped.Why)
	}
	live := entryNamed(t, mine, "Development")
	if live.Owner != OwnerInUse || live.Deletable || !strings.Contains(live.Why, "Development") {
		t.Errorf("a live folder's copy is %q/deletable=%v (%s)", live.Owner, live.Deletable, live.Why)
	}

	if _, err := DeleteDestFile("laptopcard", "my-laptop/photos", "photos", opener(root)); err != nil {
		t.Fatalf("deleting a stopped folder's copy: %v", err)
	}
	if exists(t, root, "my-laptop/photos") {
		t.Error("the stopped folder's copy is still there")
	}
	if !exists(t, root, "my-laptop/Development/notes.txt") {
		t.Error("the live folder's copy went with it")
	}
}

// THE LAST COPY OF A DELETED FILE. Retention will not remove it by age (see
// localmirror.Prune), so this view is the only place it can be let go — and it
// must be told apart from an ordinary superseded version, which belongs to a
// folder that is still being backed up.
func TestTheLastCopyOfADeletedFileIsShownAsKeptAndCanBeRemoved(t *testing.T) {
	root := fileViewDrive(t)
	dir := config.VersionRoot("my-laptop", "Development")

	l, err := ListDestFiles("laptopcard", dir, opener(root))
	if err != nil {
		t.Fatal(err)
	}
	last := entryNamed(t, l, "gone~20260101-000000.txt")
	if last.Owner != OwnerKept || !last.Deletable {
		t.Errorf("the only copy left of a deleted file is %q/deletable=%v, want kept and deletable (%s)",
			last.Owner, last.Deletable, last.Why)
	}
	superseded := entryNamed(t, l, "notes~20260101-000000.txt")
	if superseded.Owner != OwnerInUse || superseded.Deletable {
		t.Errorf("an old version of a file that is still there is %q/deletable=%v, want in-use",
			superseded.Owner, superseded.Deletable)
	}

	if _, err := DeleteDestFile("laptopcard", dir+"/gone~20260101-000000.txt", "gone~20260101-000000.txt", opener(root)); err != nil {
		t.Fatalf("deleting a version somebody chose to let go: %v", err)
	}
	if exists(t, root, dir+"/gone~20260101-000000.txt") {
		t.Error("it is still there")
	}
	if !exists(t, root, dir+"/notes~20260101-000000.txt") {
		t.Error("the other version went with it")
	}
	if !exists(t, root, "my-laptop/Development/notes.txt") {
		t.Error("the live file went with it")
	}
}

// A destination can be shared by several computers. Another computer's backups
// are shown — so the drive's contents are honestly reported — but they are
// never deleted from here: that machine is still writing to them, and its
// dashboard would go on saying the backup is fine.
func TestAnotherComputersBackupsAreNeverDeletedFromHere(t *testing.T) {
	root := fileViewDrive(t)

	theirs, err := ListDestFiles("laptopcard", "attic-pi", opener(root))
	if err != nil {
		t.Fatal(err)
	}
	e := entryNamed(t, theirs, "photos")
	if e.Deletable {
		t.Error("offered to delete another computer's backup")
	}
	if !strings.Contains(e.Why, "attic-pi") {
		t.Errorf("the explanation does not say whose backups these are: %q", e.Why)
	}

	if _, err := DeleteDestFile("laptopcard", "attic-pi/photos", "photos", opener(root)); err == nil {
		t.Fatal("deleted another computer's backup")
	}
	if !exists(t, root, "attic-pi/photos/theirs.jpg") {
		t.Error("another computer's backup was removed")
	}
}

// The page is written against this payload, key for key. Asserted on the
// marshalled JSON rather than the struct, because a renamed tag compiles
// perfectly and breaks the view.
func TestTheFileViewsJSONIsTheShapeThePageIsWrittenAgainst(t *testing.T) {
	root := fileViewDrive(t)

	top, err := ListDestFiles("laptopcard", "", opener(root))
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(top)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]json.RawMessage
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"target", "path", "parent", "entries"} {
		if _, ok := got[key]; !ok {
			t.Errorf("the listing has no %q: %s", key, raw)
		}
	}
	// The top level's parent is null, not "" — there is nothing above it.
	if string(got["parent"]) != "null" {
		t.Errorf("the top level's parent is %s, want null", got["parent"])
	}

	inside, err := ListDestFiles("laptopcard", "my-laptop", opener(root))
	if err != nil {
		t.Fatal(err)
	}
	raw, err = json.Marshal(inside.Entries[0])
	if err != nil {
		t.Fatal(err)
	}
	var row map[string]json.RawMessage
	if err := json.Unmarshal(raw, &row); err != nil {
		t.Fatal(err)
	}
	want := []string{"name", "rel", "dir", "size", "modified", "kind", "owner", "why", "deletable"}
	for _, key := range want {
		if _, ok := row[key]; !ok {
			t.Errorf("a row has no %q: %s", key, raw)
		}
	}
	if len(row) != len(want) {
		t.Errorf("a row carries %d fields, want exactly %v: %s", len(row), want, raw)
	}

	// An empty directory is [] and never null: the page iterates it.
	writeUnder(t, root, "my-laptop/Development/empty/.keep", "")
	if err := os.Remove(filepath.Join(root, "my-laptop", "Development", "empty", ".keep")); err != nil {
		t.Fatal(err)
	}
	empty, err := ListDestFiles("laptopcard", "my-laptop/Development/empty", opener(root))
	if err != nil {
		t.Fatal(err)
	}
	raw, err = json.Marshal(empty)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"entries":[]`) {
		t.Errorf("an empty directory came back as %s", raw)
	}
}

// noReadDir hides a backend's ReadDir, leaving only the Backend interface — the
// state every implementation was in before DirLister existed.
type noReadDir struct{ localmirror.Backend }

// The listing must not depend on a backend being able to list one directory:
// Backend deliberately has no ReadDir, and the fallback walk has to stop at the
// first level rather than turning a click into a walk of the destination.
func TestListingWorksOnABackendThatCannotListOneDirectory(t *testing.T) {
	root := fileViewDrive(t)
	open := func(config.Target) (localmirror.Backend, error) {
		return noReadDir{localmirror.NewLocalFS(root)}, nil
	}

	l, err := ListDestFiles("laptopcard", "my-laptop", open)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"Development", "photos"}; strings.Join(names(l.Entries), ",") != strings.Join(want, ",") {
		t.Errorf("listed %v, want %v", names(l.Entries), want)
	}
	for _, e := range l.Entries {
		if strings.Contains(e.Rel, "notes.txt") {
			t.Error("the fallback descended into a subdirectory; one click must list one directory")
		}
	}
}

// Sorting is part of the contract the page is written against.
func TestTheFileViewSortsDirectoriesFirst(t *testing.T) {
	root := fileViewDrive(t)
	writeUnder(t, root, "my-laptop/Development/zzz.txt", "a file")
	writeUnder(t, root, "my-laptop/Development/aaa/deep.txt", "in a directory")

	l, err := ListDestFiles("laptopcard", "my-laptop/Development", opener(root))
	if err != nil {
		t.Fatal(err)
	}
	if got := names(l.Entries); strings.Join(got, ",") != "aaa,notes.txt,zzz.txt" {
		t.Errorf("listed %v, want the directory first and then the files by name", got)
	}
	if l.Entries[0].Kind != KindDir || l.Entries[1].Kind != KindFile {
		t.Errorf("kinds are %q and %q", l.Entries[0].Kind, l.Entries[1].Kind)
	}
}

// The one place this package walks a destination is the mirror pass, and it is
// not this. A click lists one directory: the listing must never descend, or
// opening a machine's directory on a share would cost the 26 seconds a whole
// destination index costs.
func TestBrowsingNeverWalksTheTree(t *testing.T) {
	root := fileViewDrive(t)
	counted := &countingLister{Backend: localmirror.NewLocalFS(root), root: root}
	open := func(config.Target) (localmirror.Backend, error) { return counted, nil }

	if _, err := ListDestFiles("laptopcard", "my-laptop", open); err != nil {
		t.Fatal(err)
	}
	if counted.walks != 0 {
		t.Errorf("listing one directory walked the tree %d times", counted.walks)
	}
	// One for the directory asked for, and no more: the version store's extra
	// listing only applies inside it.
	if counted.reads != 1 {
		t.Errorf("listing one directory made %d directory listings, want 1", counted.reads)
	}
}

// countingLister answers like a local drive and counts what it was asked for,
// so a listing that quietly grew into a walk shows up as a number.
type countingLister struct {
	localmirror.Backend
	root  string
	reads int
	walks int
}

func (c *countingLister) ReadDir(p string) ([]fs.DirEntry, error) {
	c.reads++
	return os.ReadDir(filepath.Join(c.root, filepath.FromSlash(p)))
}

func (c *countingLister) WalkDir(root string, fn fs.WalkDirFunc) error {
	c.walks++
	return c.Backend.WalkDir(root, fn)
}
