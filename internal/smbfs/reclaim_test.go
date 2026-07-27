// SPDX-License-Identifier: MIT

package smbfs

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/phil9922/backup-maker/internal/localmirror"
)

// Space reclamation had only ever run against a local disk. Deleting over the
// network is the part that was actually in doubt: SMB has its own error
// vocabulary, its own idea of what a directory walk returns, and a server that
// may quietly ignore requests the local filesystem honours. These tests drive
// the real reclaimer against a real SMB server, and derive their expectations
// from what the server reports rather than from what the client asked for.
//
// Each test isolates ONE invariant with a fixture sized so that the reclaimer
// must actually reach the behaviour under test. An earlier draft used a single
// combined fixture with a small byte budget, and mutation testing showed the
// round-robin loop finished in one pass — so "keep the newest snapshot" and
// "spread deletion across folders" were never exercised and three deliberately
// broken versions of the reclaimer still passed. Budgets below are chosen to
// force the specific pass being tested.
//
// Skipped unless BM_SMB_TEST_URL (+_USER/_PASS) is set.
//
// Prefer a real Samba server: impacket's is easier to start but answers SMB2
// filesystem-info queries by comparing against SMB1 constants, so it cannot
// answer FileFsFullSizeInformation at all and TestSMBUsageReportsRealSpace
// skips against it. It also ignores Chtimes, which real Samba honours — so the
// engine's mtime calibration takes a different path there than it does here.
//
// Samba, running as an ordinary user on a spare port (no root, no touching the
// system smbd). Keep the directory path short: Samba's messaging sockets hit
// the ~107-character UNIX socket limit and fail with "File name too long".
//
//	D=/tmp/bmsmb; mkdir -p $D/{priv,lock,state,cache,run,log,share,ncalrpc}
//	# smb.conf: point private/lock/state/cache/pid/ncalrpc dir at $D, share $D/share
//	printf 'pw\npw\n' | pdbedit --configfile=$D/smb.conf -a -u "$USER" -t
//	smbd -F --no-process-group -s $D/smb.conf -p 4456 -l $D/log
//	BM_SMB_TEST_URL=//127.0.0.1:4456/share BM_SMB_TEST_USER=$USER BM_SMB_TEST_PASS=pw go test ./internal/smbfs
//
// Guest access does NOT work with a non-root smbd: it cannot switch to the
// guest account, and every write comes back "permission denied". Use pdbedit.
//
// impacket, if you only need the reclaim tests and not Usage():
//
//	impacket-smbserver -smb2support -port 4455 -username u -password p share /tmp/smbtest
//	BM_SMB_TEST_URL=//127.0.0.1:4455/share BM_SMB_TEST_USER=u BM_SMB_TEST_PASS=p go test ./internal/smbfs

// stampLayout mirrors the unexported constant in internal/localmirror that
// versionPath uses to name old versions. The reclaimer ages versions by parsing
// this out of the filename, so a test that builds version files has to spell it
// the same way.
const stampLayout = "20060102-150405"

func quietLog() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// smbShare connects to the test server inside a per-test subdirectory, so runs
// leave nothing behind and cannot collide with each other.
func smbShare(t *testing.T, name string) *FS {
	t.Helper()
	url := os.Getenv("BM_SMB_TEST_URL")
	if url == "" {
		t.Skip("BM_SMB_TEST_URL not set")
	}
	user, pass := os.Getenv("BM_SMB_TEST_USER"), os.Getenv("BM_SMB_TEST_PASS")

	parent, err := New(url, user, pass)
	if err != nil {
		t.Fatalf("connect to %s: %v", url, err)
	}
	_ = parent.RemoveAll(name) // a previous failed run may have left this behind
	if err := parent.MkdirAll(name); err != nil {
		parent.Close()
		t.Fatalf("mkdir %s: %v", name, err)
	}
	t.Cleanup(func() {
		_ = parent.RemoveAll(name)
		parent.Close()
	})

	f, err := New(strings.TrimRight(url, "/")+"/"+name, user, pass)
	if err != nil {
		t.Fatalf("connect to subpath %s: %v", name, err)
	}
	t.Cleanup(func() { f.Close() })
	return f
}

// connectSMB opens a second connection to a subdirectory smbShare already
// created, without disturbing its contents. Engine.Run closes the backend it is
// given, so the test needs a connection it still owns afterwards.
func connectSMB(t *testing.T, name string) *FS {
	t.Helper()
	url := os.Getenv("BM_SMB_TEST_URL")
	if url == "" {
		t.Skip("BM_SMB_TEST_URL not set")
	}
	f, err := New(strings.TrimRight(url, "/")+"/"+name, os.Getenv("BM_SMB_TEST_USER"), os.Getenv("BM_SMB_TEST_PASS"))
	if err != nil {
		t.Fatalf("connect to %s: %v", name, err)
	}
	return f
}

func writeSMB(t *testing.T, f localmirror.Backend, p string, size int) {
	t.Helper()
	if dir := filepath.ToSlash(filepath.Dir(p)); dir != "." {
		if err := f.MkdirAll(dir); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
	}
	if err := f.WriteFile(p, make([]byte, size)); err != nil {
		t.Fatalf("write %s: %v", p, err)
	}
}

func existsSMB(t *testing.T, f localmirror.Backend, p string) bool {
	t.Helper()
	_, err := f.Stat(p)
	return err == nil
}

// versionIn builds a path the reclaimer will recognise as an old version of the
// given age: .backup-maker-versions/<machine>/<label>/<file>~<stamp>.txt.
func versionIn(label, base string, ts time.Time) string {
	return localmirror.VersionsDirName + "/workstation/" + label + "/" + base + "~" + ts.Format(stampLayout) + ".txt"
}

// TestSMBUsageReportsRealSpace exercises smbfs.Usage() against a live server.
// It is the input to every reclaim decision — if it returns nonsense, the
// reclaimer either never fires or never stops.
func TestSMBUsageReportsRealSpace(t *testing.T) {
	f := smbShare(t, "usage-test")

	avail, total, err := f.Usage()
	if err != nil {
		// impacket's server dispatches SMB2 filesystem-info queries by
		// comparing the requested class against SMB1 trans2 level constants,
		// so FileFsFullSizeInformation (SMB2 class 7) never matches its
		// FILE_FS_FULL_SIZE_INFORMATION (1007) and it answers with an empty
		// buffer. Samba, Windows and NAS firmware all answer it. Skip rather
		// than fail — but loudly, because it means free-space reporting is
		// genuinely unverified against this server.
		t.Skipf("this SMB server does not answer FileFsFullSizeInformation (%v); "+
			"free-space reporting is UNVERIFIED here — point BM_SMB_TEST_URL at Samba or a real NAS to cover it", err)
	}
	if total == 0 {
		t.Fatal("Usage reported a total capacity of 0; reclaim thresholds would be meaningless")
	}
	if avail == 0 {
		t.Error("Usage reported 0 bytes available on a share that is clearly writable")
	}
	if avail > total {
		t.Errorf("Usage reported more available (%d) than total (%d)", avail, total)
	}
	t.Logf("SMB share reports %d bytes available of %d total", avail, total)
}

// TestSMBReclaimTakesOldestFirstAndSparesTheMirror covers the two rules that
// matter most when the budget is small: take the oldest history first, and
// never touch the live mirror. The mirror file is the largest thing on the
// share, so a reclaimer that ranged over the whole destination would reach for
// it immediately.
func TestSMBReclaimTakesOldestFirstAndSparesTheMirror(t *testing.T) {
	f := smbShare(t, "oldest-first")
	now := time.Now()

	writeSMB(t, f, "workstation/code/keep-me.txt", 9000)

	oldest := versionIn("code", "a.txt", now.Add(-96*time.Hour))
	middle := versionIn("code", "b.txt", now.Add(-72*time.Hour))
	newest := versionIn("code", "c.txt", now.Add(-2*time.Hour))
	for _, p := range []string{oldest, middle, newest} {
		writeSMB(t, f, p, 1000)
	}

	// Budget for exactly one file, so the choice of which is unambiguous.
	freed, deleted := localmirror.NewReclaimer().Reclaim(f, 1000, now, quietLog())
	if deleted != 1 || freed != 1000 {
		t.Fatalf("expected exactly one 1000-byte file to be reclaimed over SMB, got %d files / %d bytes", deleted, freed)
	}

	if !existsSMB(t, f, "workstation/code/keep-me.txt") {
		t.Error("the live mirror was deleted — the one thing reclaiming must never do")
	}
	if existsSMB(t, f, oldest) {
		t.Error("the oldest version survived while something else was deleted")
	}
	if !existsSMB(t, f, middle) || !existsSMB(t, f, newest) {
		t.Error("a newer version was deleted while the oldest was still available")
	}
}

// TestSMBReclaimSpreadsAcrossFolders checks the fairness rule: draining one
// folder's entire history to spare another's is not a decision this tool makes
// for the user. The fixture holds only versions, in two folders, with a budget
// for exactly two files — so a fair reclaimer takes one from each and a greedy
// one takes two from the first.
func TestSMBReclaimSpreadsAcrossFolders(t *testing.T) {
	f := smbShare(t, "spread")
	now := time.Now()

	codeOldest := versionIn("code", "a.txt", now.Add(-96*time.Hour))
	codeNext := versionIn("code", "b.txt", now.Add(-72*time.Hour))
	docsOldest := versionIn("docs", "d.txt", now.Add(-96*time.Hour))
	docsNext := versionIn("docs", "e.txt", now.Add(-72*time.Hour))
	for _, p := range []string{codeOldest, codeNext, docsOldest, docsNext} {
		writeSMB(t, f, p, 1000)
	}

	freed, deleted := localmirror.NewReclaimer().Reclaim(f, 2000, now, quietLog())
	if deleted != 2 || freed != 2000 {
		t.Fatalf("expected exactly two 1000-byte files reclaimed, got %d files / %d bytes", deleted, freed)
	}

	if existsSMB(t, f, codeOldest) || existsSMB(t, f, docsOldest) {
		t.Error("deletion was not spread across folders: one folder kept its oldest version while the other lost two")
	}
	if !existsSMB(t, f, codeNext) || !existsSMB(t, f, docsNext) {
		t.Error("a folder was drained past its oldest version while the other folder still had history to give")
	}
}

// TestSMBReclaimKeepsNewestSnapshot checks the rule that protects timed
// backups: a job may lose old snapshots but never its newest, and a job with a
// single snapshot is untouchable. The budget deliberately exceeds everything
// deletable, so the reclaimer sweeps every eligible zip and the only files left
// standing are the ones the rule protects.
func TestSMBReclaimKeepsNewestSnapshot(t *testing.T) {
	f := smbShare(t, "snapshots")
	now := time.Now()

	// The server ignores Chtimes, so these are aged by the modification time
	// the server itself assigns; the expectation is read back below rather
	// than assumed.
	zips := []string{
		localmirror.ArchivesDirName + "/nightly/nightly-1.zip",
		localmirror.ArchivesDirName + "/nightly/nightly-2.zip",
		localmirror.ArchivesDirName + "/nightly/nightly-3.zip",
	}
	for i, p := range zips {
		writeSMB(t, f, p, 1000)
		if i < len(zips)-1 {
			time.Sleep(1100 * time.Millisecond) // distinct server-side mtimes
		}
	}
	lone := localmirror.ArchivesDirName + "/weekly/weekly-1.zip"
	writeSMB(t, f, lone, 1000)

	newestZip, newestMod := "", time.Time{}
	for _, p := range zips {
		fi, err := f.Stat(p)
		if err != nil {
			t.Fatalf("stat %s: %v", p, err)
		}
		if fi.ModTime().After(newestMod) {
			newestZip, newestMod = p, fi.ModTime()
		}
	}
	t.Logf("server considers %s the newest snapshot (%s)", newestZip, newestMod)

	// More than the 2000 bytes that are actually eligible, so the reclaimer
	// takes everything it is allowed to take and stops.
	freed, deleted := localmirror.NewReclaimer().Reclaim(f, 100000, now, quietLog())
	t.Logf("reclaimed %d bytes by deleting %d snapshots over SMB", freed, deleted)

	if !existsSMB(t, f, newestZip) {
		t.Errorf("the newest snapshot %s was deleted even though a job must always keep its latest", newestZip)
	}
	if !existsSMB(t, f, lone) {
		t.Error("a snapshot job's only zip was deleted, leaving that job with no protection at all")
	}
	survivors := 0
	for _, p := range zips {
		if existsSMB(t, f, p) {
			survivors++
		}
	}
	if survivors != 1 {
		t.Errorf("expected exactly one of the three nightly snapshots to survive, %d did", survivors)
	}
	if deleted != 2 || freed != 2000 {
		t.Errorf("expected the two older snapshots (2000 bytes) to be reclaimed, got %d files / %d bytes", deleted, freed)
	}
}

// stubbornFS is a real SMB filesystem on which deleting certain files fails,
// standing in for the half-broken permissions and locked files a real network
// share produces. Only Remove is affected.
type stubbornFS struct {
	*FS
	failOn string
}

func (s *stubbornFS) Remove(p string) error {
	if strings.Contains(p, s.failOn) {
		return fmt.Errorf("simulated network failure deleting %s", p)
	}
	return s.FS.Remove(p)
}

// TestSMBReclaimDoesNotCountFailedDeletes covers the failure mode that matters
// on a network filesystem specifically: a delete that does not succeed must not
// be counted as space freed. If it were, the engine would believe it had made
// room, stop reclaiming, and then fail the write anyway — reporting a healthy
// destination that cannot actually be written to.
func TestSMBReclaimDoesNotCountFailedDeletes(t *testing.T) {
	base := smbShare(t, "failed-delete")
	f := &stubbornFS{FS: base, failOn: "undeletable"}
	now := time.Now()

	// Two folders, so one group refusing to give up a file does not end the
	// pass before the other group is reached.
	stuck := versionIn("code", "undeletable.txt", now.Add(-96*time.Hour))
	good := versionIn("docs", "fine.txt", now.Add(-72*time.Hour))
	writeSMB(t, f, stuck, 5000)
	writeSMB(t, f, good, 1000)

	freed, deleted := localmirror.NewReclaimer().Reclaim(f, 1000, now, quietLog())

	if freed != 1000 || deleted != 1 {
		t.Errorf("reclaimer reported %d bytes freed across %d files; only the 1000-byte file was actually deletable",
			freed, deleted)
	}
	if !existsSMB(t, f, stuck) {
		t.Error("the file whose deletion failed is gone, so the failure was not real")
	}
	if existsSMB(t, f, good) {
		t.Error("the reclaimer stopped at the failed delete and never freed the space it could have")
	}
}

// squeezedFS is a real SMB filesystem that reports itself as full. Only the
// free-space reading is synthetic; every read, write, walk and delete still
// crosses the network.
type squeezedFS struct {
	*FS
}

func (s *squeezedFS) Usage() (avail, total uint64, err error) { return 0, 64 << 20, nil }

// TestSMBEngineReclaimsUnderPressure is the end-to-end path the product
// actually takes: the engine reads free space off the SMB share, finds it below
// the configured reserve, reclaims over the network, and completes the backup.
// Nothing before this had connected Usage() to Reclaim() over a real network
// filesystem.
func TestSMBEngineReclaimsUnderPressure(t *testing.T) {
	f := smbShare(t, "engine-test")

	const uuid = "smb-reclaim-test-uuid"
	if err := localmirror.WriteMarker(f, uuid, "workstation"); err != nil {
		t.Fatalf("write target marker: %v", err)
	}

	// Roughly 1.5MB of deletable history, in units small enough that the
	// reclaimer has to delete several to satisfy the shortfall.
	const versionSize = 300 << 10
	var history []string
	for i, base := range []string{"v1", "v2", "v3", "v4", "v5"} {
		p := versionIn("code", base+".txt", time.Now().Add(-time.Duration(96-i*6)*time.Hour))
		writeSMB(t, f, p, versionSize)
		history = append(history, p)
	}

	src := t.TempDir()
	if err := os.WriteFile(filepath.Join(src, "new.bin"), make([]byte, 64<<10), 0o644); err != nil {
		t.Fatal(err)
	}
	// A file that is already mirrored and still present in the source, so this
	// test independently checks that a live backup is not collateral damage of
	// reclaiming under pressure. It must exist on both sides: a destination
	// file with no source counterpart is legitimately versioned away by the
	// mirror, which is a different mechanism entirely.
	if err := os.WriteFile(filepath.Join(src, "already-there.bin"), make([]byte, 128<<10), 0o644); err != nil {
		t.Fatal(err)
	}
	writeSMB(t, f, "workstation/code/already-there.bin", 128<<10)

	// Pressure is the one thing that cannot be staged honestly here: the share
	// is backed by a real disk with real free space, and the test server does
	// not answer FileFsFullSizeInformation anyway. So the free-space reading is
	// pinned to zero against a 1MB reserve — the exact comparison a full disk
	// trips — while every other operation the engine performs (directory walk,
	// delete, copy, verify read-back) goes over the real network filesystem.
	//
	// Run owns its backend and closes it, so give it a connection of its own
	// and keep f for assertions.
	engineFS := &squeezedFS{FS: connectSMB(t, "engine-test")}
	e := localmirror.New(localmirror.Options{
		FolderID:     "f1",
		TargetName:   "smb-dest",
		TargetType:   "share",
		SourcePath:   src,
		Backend:      engineFS,
		MachineName:  "workstation",
		Label:        "code",
		UUID:         uuid,
		MaxAgeDays:   30,
		Verify:       true,
		MinFreeBytes: 1 << 20,
		Reclaimer:    localmirror.NewReclaimer(),
		Log:          quietLog(),
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go e.Run(ctx)

	deadline := time.Now().Add(60 * time.Second)
	for !existsSMB(t, f, "workstation/code/new.bin") && time.Now().Before(deadline) {
		time.Sleep(200 * time.Millisecond)
	}
	if !existsSMB(t, f, "workstation/code/new.bin") {
		t.Fatal("the backup never completed over SMB while the destination was under its free-space reserve")
	}

	if !existsSMB(t, f, "workstation/code/already-there.bin") {
		t.Error("an existing mirrored file was deleted to make room — the live mirror is the backup")
	}

	survivors := 0
	for _, p := range history {
		if existsSMB(t, f, p) {
			survivors++
		}
	}
	if survivors == len(history) {
		t.Error("the engine saw free space below the reserve on an SMB share and deleted no history")
	}
	deletedBytes := (len(history) - survivors) * versionSize
	if deletedBytes < 1<<20 {
		t.Errorf("freed about %d bytes over SMB, short of the 1MB shortfall the engine was facing", deletedBytes)
	}
	t.Logf("%d of %d version files reclaimed over SMB (~%dKB)", len(history)-survivors, len(history), deletedBytes>>10)

	when, note := e.ReclaimNote()
	if note == "" || when.IsZero() {
		t.Error("history was deleted over SMB but the dashboard would show no reclaim note; deletion must never be silent")
	}
	t.Logf("reclaim note: %q", note)
}
