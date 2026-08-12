// SPDX-License-Identifier: MIT

package archive

import (
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/phil9922/backup-maker/internal/config"
	"github.com/phil9922/backup-maker/internal/localmirror"
)

// THE GUARANTEE THIS FILE EXISTS FOR: a snapshot that will not fit makes room
// from its OWN history, and if it still will not fit it stops before writing
// anything.
//
// THE BUG. The writer had no space check at all. It opened the temp zip and
// wrote until it finished or the destination filled — and a full destination
// does not merely lose the snapshot, it breaks the continuous mirror to the same
// drive, so the failure takes out the backup that WAS working, and leaves a
// part-written multi-gigabyte temp behind on a disk with nothing left on it. The
// Reclaimer that exists for this was wired to the mirror only, and is off unless
// min_free_gb is set — which it is not, by default.

// fixedSpace is a destination whose free space the test dictates. Deletions are
// not reflected back into it deliberately: ensureRoomFor must credit itself with
// what it freed, since an SMB server's free-space figure can lag a delete.
type fixedSpace struct {
	localmirror.Backend
	avail uint64
}

func (f *fixedSpace) Usage() (uint64, uint64, error) { return f.avail, 500 << 30, nil }

// cannotReportSpace is a destination that does not implement SpaceReporter —
// embedding the interface promotes only Backend's own methods.
type cannotReportSpace struct{ localmirror.Backend }

// usageFails is a destination that has the method and cannot answer with it.
type usageFails struct{ localmirror.Backend }

func (usageFails) Usage() (uint64, uint64, error) {
	return 0, 0, errors.New("statfs: no such device")
}

const gib = int64(1) << 30

// sparseFile makes a file of the given apparent size without using the disk, so
// a test can stage gigabytes of snapshot history in a temp directory. Nothing in
// the space check reads these; it stats them.
func sparseFile(t *testing.T, path string, size int64) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Truncate(size); err != nil {
		f.Close()
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
}

// stageZips creates finished snapshots for a job, named as real runs name them.
func stageZips(t *testing.T, dst, job string, stampsAndSizes map[string]int64) string {
	t.Helper()
	dir := filepath.Join(dst, DirName, "mach", job)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for stamp, size := range stampsAndSizes {
		sparseFile(t, filepath.Join(dir, job+"-"+stamp+".zip"), size)
	}
	return dir
}

func namesIn(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, e := range entries {
		out = append(out, e.Name())
	}
	return out
}

func has(names []string, want string) bool {
	for _, n := range names {
		if n == want {
			return true
		}
	}
	return false
}

// The headline case, and the one the owner asked for in as many words: "it
// should delete the oldest version if it's going to run out of space."
func TestASnapshotThatWouldNotFitDeletesItsOwnOldestFirst(t *testing.T) {
	cfg, job, b, dst := testSetup(t)
	// Retention keeps ten, so end-of-run pruning cannot delete any of these:
	// whatever goes, went because the snapshot needed the room.
	job.Keep = 10
	dir := stageZips(t, dst, "testjob", map[string]int64{
		"20260101-010000": gib, // oldest
		"20260102-010000": gib,
		"20260103-010000": gib, // newest — the estimate comes from this one
	})

	// Estimate 1GB + 1/8, plus a 2GB floor = 3.125GB wanted, 1.5GB free. Two
	// deletions get there; the third snapshot must survive.
	b = &fixedSpace{Backend: b, avail: uint64(gib + gib/2)}

	res := Run(b, cfg, job, "hunter2", slog.New(slog.DiscardHandler), nil)
	if res.Err != "" {
		t.Fatalf("the run failed instead of making room: %s", res.Err)
	}
	names := namesIn(t, dir)
	if has(names, "testjob-20260101-010000.zip") || has(names, "testjob-20260102-010000.zip") {
		t.Errorf("the oldest snapshots were not deleted to make room: %v", names)
	}
	if !has(names, "testjob-20260103-010000.zip") {
		t.Errorf("the NEWEST snapshot was deleted while older ones survived: %v", names)
	}
	if res.File == "" {
		t.Error("no snapshot was written after room was made")
	}
}

// A backup program may not leave somebody with none. This is the same line
// reclaim.go draws at `if len(zips) <= 1 { continue }`, and it holds even when
// obeying it means the run cannot happen at all.
func TestMakingRoomNeverDeletesAJobsOnlySnapshot(t *testing.T) {
	cfg, job, b, dst := testSetup(t)
	dir := stageZips(t, dst, "testjob", map[string]int64{"20260101-010000": 4 * gib})

	res := Run(&fixedSpace{Backend: b, avail: 0}, cfg, job, "hunter2",
		slog.New(slog.DiscardHandler), nil)
	if res.Err == "" {
		t.Fatal("a snapshot was written to a destination with no room at all")
	}
	if names := namesIn(t, dir); !has(names, "testjob-20260101-010000.zip") {
		t.Fatalf("THE JOB'S ONLY SNAPSHOT WAS DELETED, leaving no backup at all: %v", names)
	}
}

// The Reclaimer spreads deletion round-robin across every job and every folder's
// version history. That is right for the mirror's space pressure and wrong here:
// somebody set up job A and job B, and B quietly losing its history because A
// grew is not something they asked for.
func TestMakingRoomNeverTouchesAnotherJobsSnapshots(t *testing.T) {
	cfg, job, b, dst := testSetup(t)
	job.Keep = 10 // as above: retention must not be what deletes anything here
	mine := stageZips(t, dst, "testjob", map[string]int64{
		"20260101-010000": gib, // the only one this run may delete
		"20260102-010000": gib,
	})
	theirs := stageZips(t, dst, "otherjob", map[string]int64{
		"20250101-010000": 4 * gib,
		"20250102-010000": 4 * gib,
		"20250103-010000": 4 * gib,
	})
	// And the version store, the other thing the Reclaimer is allowed to eat.
	versions := filepath.Join(dst, localmirror.VersionsDirName, "mach", "proj")
	if err := os.MkdirAll(versions, 0o755); err != nil {
		t.Fatal(err)
	}
	sparseFile(t, filepath.Join(versions, "a~20250101-000000.txt"), 4*gib)

	// 3.125GB wanted, 2.2GB free: one deletion of this job's own is enough.
	b = &fixedSpace{Backend: b, avail: uint64(gib*2 + gib/5)}
	res := Run(b, cfg, job, "hunter2", slog.New(slog.DiscardHandler), nil)
	if res.Err != "" {
		t.Fatalf("the run failed instead of making room from its own history: %s", res.Err)
	}

	if names := namesIn(t, mine); has(names, "testjob-20260101-010000.zip") {
		t.Errorf("nothing was deleted, so this test proves nothing: %v", names)
	}
	if names := namesIn(t, theirs); len(names) != 3 {
		t.Errorf("another schedule's snapshots were deleted: %v", names)
	}
	if names := namesIn(t, versions); len(names) != 1 {
		t.Errorf("the version store was raided by a snapshot making room: %v", names)
	}
}

// The whole point of checking BEFORE the write. A part-written 90GB temp on a
// full disk is the worst possible outcome, and it is what happened without this.
func TestASnapshotThatStillWillNotFitFailsBeforeWritingAnything(t *testing.T) {
	cfg, job, b, dst := testSetup(t)

	res := Run(&fixedSpace{Backend: b, avail: 0}, cfg, job, "hunter2",
		slog.New(slog.DiscardHandler), nil)
	if res.Err == "" {
		t.Fatal("a snapshot was written to a destination with no room at all")
	}
	if !strings.Contains(res.Err, "has not got room") {
		t.Errorf("the failure does not say the destination is out of room: %s", res.Err)
	}
	if !strings.Contains(res.Err, "free") {
		t.Errorf("the failure says neither what was needed nor what is free: %s", res.Err)
	}
	if temps := tempsIn(t, dst); len(temps) != 0 {
		t.Fatalf("a temp file was created by a run that could not fit: %v", temps)
	}
	err := filepath.WalkDir(dst, func(p string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			t.Errorf("a run that could not fit wrote %s", p)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// Refusing to back up because free space is unknown would stop backups that have
// been working for months — a far worse failure than the one being prevented.
func TestABackendThatCannotReportSpaceStillWritesTheSnapshot(t *testing.T) {
	cfg, job, b, _ := testSetup(t)
	if _, ok := b.(localmirror.SpaceReporter); !ok {
		t.Fatal("fixture: the local backend stopped reporting space, so the wrappers below prove nothing")
	}

	for _, tc := range []struct {
		name string
		b    localmirror.Backend
	}{
		{"no Usage method at all", &cannotReportSpace{Backend: b}},
		{"Usage that returns an error", usageFails{Backend: b}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			job := job
			job.Name = "job-" + strings.ReplaceAll(tc.name, " ", "-")
			res := Run(tc.b, cfg, job, "hunter2", slog.New(slog.DiscardHandler), nil)
			if res.Err != "" {
				t.Fatalf("a destination that cannot report space blocked the snapshot: %s", res.Err)
			}
			if res.File == "" {
				t.Error("no snapshot was written")
			}
		})
	}
}

// What the last identical run COST is a far better answer than what the source
// weighs uncompressed, and the difference decides whether history gets deleted.
func TestTheEstimateUsesThePreviousZipRatherThanTheUncompressedSource(t *testing.T) {
	cfg, job, b, dst := testSetup(t)
	// A source far larger than any zip of it, staged sparsely so nothing has to
	// be written or read to make the two figures differ by 40GB.
	sparseFile(t, filepath.Join(cfg.Folders[0].Path, "big.bin"), 40*gib)
	// Both runs below are meant to stop before the write. The refusal underneath
	// is a backstop for the day the space check stops running: without it, a
	// regression here does not fail this test, it spends two minutes deflating
	// 40GB of holes first.
	full := func() localmirror.Backend {
		return &fixedSpace{Backend: &refusesToWrite{Backend: b}, avail: 0}
	}

	// FIRST RUN: nothing to have learned from, so the uncompressed total is the
	// fallback — 40GB plus the margin.
	first := Run(full(), cfg, job, "hunter2",
		slog.New(slog.DiscardHandler), nil)
	if first.Err == "" {
		t.Fatal("fixture: this run was supposed to fail for want of room")
	}
	if !strings.Contains(first.Err, "45.0GB") {
		t.Errorf("the first run did not estimate from the uncompressed source total: %s", first.Err)
	}

	// SAME SOURCE, but this job has a 1GB snapshot from yesterday. That is what
	// the run will actually cost, and the estimate must say so.
	stageZips(t, dst, "testjob", map[string]int64{"20260101-010000": gib})
	second := Run(full(), cfg, job, "hunter2", slog.New(slog.DiscardHandler), nil)
	if second.Err == "" {
		t.Fatal("fixture: this run was supposed to fail for want of room too")
	}
	if strings.Contains(second.Err, "45.0GB") || !strings.Contains(second.Err, "1.1GB") {
		t.Errorf("the estimate ignored this job's previous zip and used the source total: %s", second.Err)
	}

	// And exactly, without the message in the way.
	if got := estimateSnapshotBytes(cfg, job, cfg.FoldersForArchive(job),
		[]snapshotZip{{path: "old.zip", size: 800}, {path: "new.zip", size: 1600}}); got != 1800 {
		t.Errorf("estimate = %d, want 1800 (the NEWEST zip, 1600, plus the growth margin)", got)
	}
}

// The floor exists so a snapshot cannot drive a destination to literally zero
// and take the continuous mirror down with it — but a destination that says how
// much room it wants kept free must be believed instead.
func TestTheDestinationsOwnMinFreeSettingWinsOverTheFloor(t *testing.T) {
	cfg, job, _, _ := testSetup(t)
	if got := snapshotFloor(cfg, job); got != minFreeFloorBytes {
		t.Errorf("with no target configured the floor is %d, want %d", got, minFreeFloorBytes)
	}

	ten := 10
	cfg.Targets = []config.Target{{Type: "drive", Name: "t", Path: "/mnt/x", MinFreeGB: &ten}}
	if got := snapshotFloor(cfg, job); got != uint64(ten)<<30 {
		t.Errorf("the destination's min_free_gb was ignored: floor = %d", got)
	}

	// min_free_gb = 0 means "do not delete history for the MIRROR". It is not a
	// licence to fill the destination to the last byte from under it.
	zero := 0
	cfg.Targets[0].MinFreeGB = &zero
	if got := snapshotFloor(cfg, job); got != minFreeFloorBytes {
		t.Errorf("min_free_gb = 0 left no headroom at all: floor = %d", got)
	}
}
