// SPDX-License-Identifier: MIT

package archive

import (
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	zip "github.com/yeka/zip"

	"github.com/phil9922/backup-maker/internal/localmirror"
)

// THE GUARANTEES THIS FILE EXISTS FOR.
//
// Verification spools the WHOLE archive to a local temp file, because a zip is
// read through its central directory and that needs random access. Nothing
// checked whether the local disk had room for it: a 57GB snapshot was being
// spooled into /tmp on a partition with 34GB free — the same partition as the
// home directory the 113GB of source data lives on. Filling it is worse than
// any backup failure, because it takes out the machine the originals are on.
//
// So: the room is asked for BEFORE PACKING (two hours of packing must not be
// thrown away by a check that could have been made first), a shortfall skips
// verification rather than failing the run (an unchecked snapshot is worth far
// more than none), the disk is measured again as the spool is written, and the
// directory it is written to can be moved off the OS disk — but never into a
// folder that is being backed up.

// swapLocalUsage dictates what the local disk reports for the duration of one
// test. There is no backend to wrap here the way a destination has one: the
// spool goes to this machine's own filesystem.
func swapLocalUsage(t *testing.T, fn func(string) (uint64, uint64, error)) {
	t.Helper()
	prev := localUsage
	localUsage = fn
	t.Cleanup(func() { localUsage = prev })
}

// fixedLocal is a local disk with a dictated amount of room left.
func fixedLocal(free uint64) func(string) (uint64, uint64, error) {
	return func(string) (uint64, uint64, error) { return free, 500 << 30, nil }
}

// watchesVerification wraps a destination so a test can see whether the finished
// archive was read back at all, and what the spool directory held while it was.
// verifyZip is the only thing in a run that opens the destination for reading.
type watchesVerification struct {
	localmirror.Backend
	spoolDir string
	reads    int
	spooled  []string
}

func (w *watchesVerification) OpenRead(p string) (io.ReadCloser, error) {
	rc, err := w.Backend.OpenRead(p)
	if err != nil {
		return nil, err
	}
	w.reads++
	return &listsOnFirstRead{ReadCloser: rc, dir: w.spoolDir, into: &w.spooled}, nil
}

// listsOnFirstRead lists the spool directory the first time the archive is read
// back — the spool file is created before the first read and deleted after the
// last one, so this is the moment it exists.
type listsOnFirstRead struct {
	io.ReadCloser
	dir  string
	into *[]string
	done bool
}

func (l *listsOnFirstRead) Read(p []byte) (int, error) {
	if !l.done {
		l.done = true
		if l.dir != "" {
			for _, e := range entryNames(l.dir) {
				*l.into = append(*l.into, e)
			}
		}
	}
	return l.ReadCloser.Read(p)
}

func entryNames(dir string) []string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range entries {
		out = append(out, e.Name())
	}
	return out
}

// hasPrefix reports whether any name starts with prefix.
func hasPrefix(names []string, prefix string) bool {
	for _, n := range names {
		if strings.HasPrefix(n, prefix) {
			return true
		}
	}
	return false
}

// THE HEADLINE. 57GB of packing over wifi, two hours of it, and a local disk
// with no room to read it back: the snapshot is still written, still encrypted,
// still openable. Failing the run here would delete a complete backup because
// of a shortage somewhere else entirely.
func TestASnapshotIsStillWrittenWhenThereIsNoRoomToVerifyIt(t *testing.T) {
	cfg, job, b, dst := testSetup(t)
	job.Keep = 10 // retention must not be what deletes anything here
	// A 50GB snapshot from yesterday, staged sparsely: that is where this run's
	// estimate comes from, exactly as it does on a real machine.
	stageZips(t, dst, "testjob", map[string]int64{"20260101-010000": 50 * gib})
	// The DESTINATION has plenty of room. The laptop's own disk has 34GB.
	watched := &watchesVerification{Backend: b}
	dest := &fixedSpace{Backend: watched, avail: 500 << 30}
	swapLocalUsage(t, fixedLocal(34*uint64(gib)))

	res := Run(dest, cfg, job, "hunter2", slog.New(slog.DiscardHandler), nil)
	if res.Err != "" {
		t.Fatalf("THE RUN FAILED instead of writing an unchecked snapshot: %s", res.Err)
	}
	if res.File == "" {
		t.Fatal("no snapshot was written")
	}
	full := filepath.Join(dst, filepath.FromSlash(res.File))
	if _, err := os.Stat(full); err != nil {
		t.Fatalf("the snapshot is not on the destination: %v", err)
	}
	// And it is a real, encrypted, openable archive — not a stub kept to make
	// the run look successful.
	zr, err := zip.OpenReader(full)
	if err != nil {
		t.Fatalf("the snapshot that was kept cannot be opened: %v", err)
	}
	defer zr.Close()
	if len(zr.File) != 2 {
		t.Errorf("the snapshot holds %d entries, want 2", len(zr.File))
	}
	if watched.reads != 0 {
		t.Errorf("the archive was spooled back anyway (%d reads), which is what there was no room for", watched.reads)
	}
	if res.Unverified == "" {
		t.Error("the run claims the snapshot was checked, and it was not")
	}
}

// The unchecked state has to travel with the result, or the dashboard, the
// status command and the alerter have nothing to say it with. A run that WAS
// checked must say nothing, or the warning means nothing.
func TestAnUnverifiedSnapshotSaysSoOnTheResult(t *testing.T) {
	cfg, job, b, dst := testSetup(t)
	stageZips(t, dst, "testjob", map[string]int64{"20260101-010000": 50 * gib})
	job.Keep = 10

	watched := &watchesVerification{Backend: b}
	dest := &fixedSpace{Backend: watched, avail: 500 << 30}
	swapLocalUsage(t, fixedLocal(34*uint64(gib)))
	short := Run(dest, cfg, job, "hunter2", slog.New(slog.DiscardHandler), nil)
	if short.Err != "" {
		t.Fatalf("the run failed: %s", short.Err)
	}
	for _, want := range []string{"not checked", "56.2GB", "34.0GB", os.TempDir()} {
		if !strings.Contains(short.Unverified, want) {
			t.Errorf("the reason does not say %q, so nobody can act on it: %s", want, short.Unverified)
		}
	}

	// The same job on a machine with room says nothing at all — and really does
	// read the archive back.
	job.Name = "checked"
	swapLocalUsage(t, fixedLocal(500<<30))
	watched2 := &watchesVerification{Backend: b}
	ok := Run(&fixedSpace{Backend: watched2, avail: 500 << 30}, cfg, job, "hunter2",
		slog.New(slog.DiscardHandler), nil)
	if ok.Err != "" {
		t.Fatalf("the run failed: %s", ok.Err)
	}
	if ok.Unverified != "" {
		t.Errorf("a checked snapshot claims it was not: %s", ok.Unverified)
	}
	if watched2.reads != 1 {
		t.Errorf("the archive was read back %d times, want 1: verification did not run", watched2.reads)
	}
}

// THE PRE-FLIGHT IS AN ESTIMATE, AND IT IS TAKEN HOURS EARLY. Between it and the
// read-back the machine packs tens of gigabytes and everything else on it goes
// on writing. The spool measures the disk as it copies, and stops — it does not
// get to be the thing that fills the disk the source folders live on.
func TestTheSpoolNeverFillsTheLocalDiskToZero(t *testing.T) {
	cfg, job, b, dst := testSetup(t)
	spool := t.TempDir()
	cfg.Defaults.VerifySpoolDir = spool

	// Room when the run starts, and none by the time the archive is being read
	// back onto it.
	calls := 0
	swapLocalUsage(t, func(string) (uint64, uint64, error) {
		calls++
		if calls == 1 {
			return 500 << 30, 500 << 30, nil // the pre-flight, before packing
		}
		return 1 << 20, 500 << 30, nil // and now something else has eaten the disk
	})

	res := Run(b, cfg, job, "hunter2", slog.New(slog.DiscardHandler), nil)
	if res.Err != "" {
		t.Fatalf("THE RUN FAILED, discarding a finished snapshot: %s", res.Err)
	}
	if calls < 2 {
		t.Fatal("the disk was never measured while the spool was being written")
	}
	if res.File == "" {
		t.Fatal("the snapshot was thrown away when the local disk ran short")
	}
	if _, err := os.Stat(filepath.Join(dst, filepath.FromSlash(res.File))); err != nil {
		t.Fatalf("the snapshot is not on the destination: %v", err)
	}
	if res.Unverified == "" {
		t.Error("a snapshot that was abandoned half-way through checking claims it was checked")
	}
	// AND THE SPOOL IS GONE. Leaving tens of gigabytes of abandoned spool on a
	// disk that is already running out is the failure being guarded against.
	if left := entryNames(spool); len(left) != 0 {
		t.Errorf("the abandoned spool was left on the disk it was filling: %v", left)
	}
}

// A spool inside a folder being backed up would write tens of gigabytes into the
// very tree being snapshotted — and the next run would pack it and copy it to
// every destination configured.
func TestAVerifySpoolInsideASourceFolderIsRefused(t *testing.T) {
	cfg, job, b, _ := testSetup(t)
	src := cfg.Folders[0].Path
	inside := filepath.Join(src, "tmp")
	if err := os.MkdirAll(inside, 0o755); err != nil {
		t.Fatal(err)
	}

	for _, dir := range []string{inside, src} {
		cfg.Defaults.VerifySpoolDir = dir
		if _, err := resolveSpoolDir(cfg); err == nil {
			t.Fatalf("%s was accepted as a spool directory, and it is inside a folder being backed up", dir)
		} else if !strings.Contains(err.Error(), "being backed up") {
			t.Errorf("the refusal does not say why: %v", err)
		}
	}

	// End to end: the snapshot is still written, it is not checked, and NOTHING
	// was written into the source folder — not even the write probe.
	cfg.Defaults.VerifySpoolDir = inside
	res := Run(b, cfg, job, "hunter2", slog.New(slog.DiscardHandler), nil)
	if res.Err != "" {
		t.Fatalf("the run failed: %s", res.Err)
	}
	if res.Unverified == "" {
		t.Error("the snapshot was checked using a spool inside the folder it was made from")
	}
	if left := entryNames(inside); len(left) != 0 {
		t.Fatalf("SOMETHING WAS WRITTEN INTO THE FOLDER BEING BACKED UP: %v", left)
	}
	// The folder itself is untouched beyond what the fixture put there.
	if names := entryNames(src); len(names) != 4 { // a.txt, sub, node_modules, tmp
		t.Errorf("the source folder gained or lost entries: %v", names)
	}
}

// The setting exists so the spool can be moved off the disk the sources are on.
// It has to actually be used, and nothing may be left behind in it.
func TestAConfiguredSpoolDirectoryIsUsed(t *testing.T) {
	cfg, job, b, _ := testSetup(t)
	spool := t.TempDir()
	cfg.Defaults.VerifySpoolDir = spool

	watched := &watchesVerification{Backend: b, spoolDir: spool}
	res := Run(watched, cfg, job, "hunter2", slog.New(slog.DiscardHandler), nil)
	if res.Err != "" {
		t.Fatalf("the run failed: %s", res.Err)
	}
	if res.Unverified != "" {
		t.Fatalf("the snapshot was not checked: %s", res.Unverified)
	}
	if !hasPrefix(watched.spooled, "backup-maker-verify-") {
		t.Errorf("the archive was not spooled into the configured directory; it held %v", watched.spooled)
	}
	if left := entryNames(spool); len(left) != 0 {
		t.Errorf("the spool was left behind after the run: %v", left)
	}

	// Unset is today's behaviour: wherever the operating system puts temporary
	// files, which honours TMPDIR (the daemon runs from systemd, which sets none).
	cfg.Defaults.VerifySpoolDir = ""
	if dir, err := resolveSpoolDir(cfg); err != nil || dir != os.TempDir() {
		t.Errorf("unset spool directory = %q, %v; want %q", dir, err, os.TempDir())
	}
}

// A hand-edited path that cannot hold the spool is refused rather than quietly
// replaced by the default — which is how somebody who moved the spool off their
// OS disk on purpose would fill it anyway, having been told it was fixed.
func TestABadVerifySpoolDirectoryIsRefused(t *testing.T) {
	cfg, job, b, dst := testSetup(t)
	notADir := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(notADir, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		name, dir, says string
	}{
		{"not absolute", filepath.Join("relative", "spool"), "absolute"},
		{"missing", filepath.Join(t.TempDir(), "gone"), "cannot be used"},
		{"not a directory", notADir, "not a directory"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg.Defaults.VerifySpoolDir = tc.dir
			_, err := resolveSpoolDir(cfg)
			if err == nil {
				t.Fatalf("%q was accepted as a spool directory", tc.dir)
			}
			if !strings.Contains(err.Error(), tc.says) {
				t.Errorf("the refusal does not say what is wrong: %v", err)
			}

			// And the run still produces a backup, saying it was not checked.
			job := job
			job.Name = "job-" + strings.ReplaceAll(tc.name, " ", "-")
			watched := &watchesVerification{Backend: b}
			res := Run(watched, cfg, job, "hunter2", slog.New(slog.DiscardHandler), nil)
			if res.Err != "" {
				t.Fatalf("a misconfigured spool directory stopped the backup: %s", res.Err)
			}
			if _, err := os.Stat(filepath.Join(dst, filepath.FromSlash(res.File))); err != nil {
				t.Fatalf("no snapshot was written: %v", err)
			}
			if res.Unverified == "" || watched.reads != 0 {
				t.Errorf("the run fell back to the default spool directory instead of refusing: %q, %d reads",
					res.Unverified, watched.reads)
			}
		})
	}
}

// WHY BEFORE AND NOT AFTER. Packing 57GB over wifi took two hours. A check made
// when the spool is needed would throw all of that away — or, worse, be skipped
// as "we may as well try now that it is written" and fill the disk.
func TestTheRoomToVerifyIsCheckedBeforePackingStarts(t *testing.T) {
	cfg, job, b, _ := testSetup(t)

	packing, checkedDuringOrAfterPacking, checks := false, false, 0
	swapLocalUsage(t, func(string) (uint64, uint64, error) {
		checks++
		if packing {
			checkedDuringOrAfterPacking = true
		}
		return 1 << 20, 500 << 30, nil // a local disk with nothing left
	})

	res := Run(b, cfg, job, "hunter2", slog.New(slog.DiscardHandler), func(p Progress) {
		if p.Phase == PhasePacking && p.DoneFiles > 0 {
			packing = true
		}
	})
	if res.Err != "" {
		t.Fatalf("the run failed: %s", res.Err)
	}
	if checks == 0 {
		t.Fatal("the local disk was never measured at all")
	}
	if checkedDuringOrAfterPacking {
		t.Error("the room to check the snapshot was asked for after packing had already started")
	}
	if res.Unverified == "" {
		t.Fatal("the run reports a checked snapshot on a disk with no room to check one")
	}
}

// The floor is the point of the guard: it stops with room to spare rather than
// at the last byte, because a disk driven to zero breaks everything else on the
// machine — including whatever else backup-maker is doing.
func TestTheSpoolGuardStopsAtTheFloorRatherThanAtZero(t *testing.T) {
	dir := t.TempDir()
	swapLocalUsage(t, fixedLocal(minFreeFloorBytes-1))

	var sink strings.Builder
	g := newSpoolGuard(&sink, dir, minFreeFloorBytes)
	n, err := g.Write([]byte("the first byte of a 57GB spool"))
	var full *spoolFullError
	if err == nil {
		t.Fatal("the spool was allowed to start on a disk already below the floor")
	}
	if !strings.Contains(err.Error(), dir) {
		t.Errorf("the error does not name the disk that is filling up: %v", err)
	}
	if n != 0 || sink.Len() != 0 {
		t.Errorf("%d bytes were written after the disk was found to be full", sink.Len())
	}
	if !errors.As(err, &full) {
		t.Fatalf("the error is not a spoolFullError, so a run would fail instead of keeping the snapshot: %T", err)
	}

	// Above the floor it is an ordinary writer.
	swapLocalUsage(t, fixedLocal(minFreeFloorBytes))
	if _, err := newSpoolGuard(&sink, dir, minFreeFloorBytes).Write([]byte("ok")); err != nil {
		t.Errorf("a disk exactly at the floor was refused: %v", err)
	}
}
