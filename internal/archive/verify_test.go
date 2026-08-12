// SPDX-License-Identifier: MIT

package archive

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"hash/fnv"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	zip "github.com/yeka/zip"

	"github.com/phil9922/backup-maker/internal/localmirror"
)

// THE GUARANTEES THIS FILE EXISTS FOR.
//
// A snapshot of a home directory — 412,415 files, a 57GB encrypted zip — was
// written to a network share over 2½ hours and then the daemon was killed by
// the OOM killer while checking it. Verification spooled the whole archive to a
// local temp file and opened it with zip.NewReader, which builds the entire
// central directory in memory: one struct per entry, four hundred thousand of
// them. The run was lost, the 57GB spool stayed in /tmp because SIGKILL runs no
// deferred cleanup, and systemd restarted the daemon into an identical run.
//
// So verification streams: read once, forwards, hold nothing, write nothing to
// this computer. These tests pin the two halves of that — that it costs the same
// memory whatever the archive holds, and that it still proves what it claims:
// the password opens the archive, every entry decrypts, and a damaged or
// truncated archive is caught rather than kept.

// testEntry is one file in a staged archive.
type testEntry struct {
	name string
	data []byte
}

// stageArchive writes an encrypted zip with the same writer and the same
// encryption writeZip uses, without needing files on disk to pack.
func stageArchive(t *testing.T, path, password string, enc zip.EncryptionMethod, entries []testEntry) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	bw := bufio.NewWriterSize(f, writeBufferSize)
	zw := zip.NewWriter(bw)
	for _, e := range entries {
		w, err := zw.Encrypt(e.name, password, enc)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write(e.data); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := bw.Flush(); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
}

// stageManyEntries writes an archive of n empty entries. Empty because what is
// under test is the number of entries, not the number of bytes, and because
// every entry costs a key derivation to write and another to check.
func stageManyEntries(t *testing.T, path, password string, enc zip.EncryptionMethod, n int) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	bw := bufio.NewWriterSize(f, writeBufferSize)
	zw := zip.NewWriter(bw)
	for i := range n {
		w, err := zw.Encrypt(fmt.Sprintf("proj/dir%03d/f%06d.txt", i%500, i), password, enc)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write(nil); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := bw.Flush(); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
}

// dripsTheArchiveBack hands the archive back in small reads, the way a network
// share does, so a test can sample memory hundreds of times across a check
// rather than twice.
type dripsTheArchiveBack struct {
	localmirror.Backend
	chunk int
}

func (d dripsTheArchiveBack) OpenRead(p string) (io.ReadCloser, error) {
	rc, err := d.Backend.OpenRead(p)
	if err != nil {
		return nil, err
	}
	return &shortReads{ReadCloser: rc, chunk: d.chunk}, nil
}

type shortReads struct {
	io.ReadCloser
	chunk int
}

func (s *shortReads) Read(p []byte) (int, error) {
	if len(p) > s.chunk {
		p = p[:s.chunk]
	}
	return s.ReadCloser.Read(p)
}

// liveHeap is what is still REACHABLE, which is the question here: not how many
// bytes verification has touched, but how much of the archive it is holding on
// to while it works.
func liveHeap() uint64 {
	runtime.GC()
	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	return m.HeapAlloc
}

// heapHeadroom is how much the measurement is allowed to move between a small
// archive and one thirty times bigger. Live heap is not perfectly repeatable —
// the allocator hands back memory in chunks — so this has to be wide enough not
// to flap, and narrow enough that per-entry state cannot hide in it: 6,000
// entries of anything zip.NewReader kept (a header struct and a name is well
// over 200 bytes) is more than a megabyte.
const heapHeadroom = 512 << 10

// THE HEADLINE. The check must cost the same whether the snapshot holds two
// files or four hundred thousand, because the machine it runs on has 30GB of RAM
// and the snapshot it was killed checking was 412,415 entries.
func TestVerifyingAHugeArchiveDoesNotHoldItInMemory(t *testing.T) {
	few := heapWhileChecking(t, 200)
	many := heapWhileChecking(t, 6000)
	if many > few+heapHeadroom {
		t.Errorf("checking 200 entries held %d bytes and checking 6000 held %d: memory is growing with the entry count, "+
			"which is what the OOM killer stopped", few, many)
	}
	t.Logf("live heap while checking: 200 entries %d bytes, 6000 entries %d bytes", few, many)
}

// heapWhileChecking verifies an archive of n entries and returns the most memory
// that was live at any point through its second half — by which point anything
// accumulating per entry has accumulated most of it.
func heapWhileChecking(t *testing.T, entries int) uint64 {
	t.Helper()
	dir := t.TempDir()
	// AES-128 rather than the AES-256 a real snapshot uses: the key derivation
	// is 1000 rounds per entry at either strength, and halving it halves the
	// time this test spends proving something that has nothing to do with the
	// key size.
	stageManyEntries(t, filepath.Join(dir, "snap.zip"), "hunter2", zip.AES128Encryption, entries)
	fi, err := os.Stat(filepath.Join(dir, "snap.zip"))
	if err != nil {
		t.Fatal(err)
	}
	b := dripsTheArchiveBack{Backend: localmirror.NewLocalFS(dir), chunk: 4 << 10}

	var peak uint64
	if err := verifyZip(b, "snap.zip", "hunter2", entries, func(read int64) {
		if read*2 > fi.Size() {
			if h := liveHeap(); h > peak {
				peak = h
			}
		}
	}); err != nil {
		t.Fatalf("a good archive of %d entries failed its check: %v", entries, err)
	}
	if peak == 0 {
		t.Fatal("memory was never sampled, so this test proves nothing")
	}
	return peak
}

// EVERY entry, not the first one and not a sample. A snapshot is one file that
// is either restorable or is not, and an archive whose four hundred thousandth
// entry is corrupt is not restorable.
func TestVerificationStillProvesEveryEntryDecrypts(t *testing.T) {
	dir := t.TempDir()
	entries := []testEntry{
		{"proj/a.txt", []byte("alpha")},
		{"proj/sub/b.txt", bytes.Repeat([]byte("beta "), 4000)},
		{"proj/c.txt", []byte("gamma")},
	}
	stageArchive(t, filepath.Join(dir, "snap.zip"), "hunter2", zip.AES256Encryption, entries)
	good, err := os.ReadFile(filepath.Join(dir, "snap.zip"))
	if err != nil {
		t.Fatal(err)
	}
	b := localmirror.NewLocalFS(dir)
	if err := verifyZip(b, "snap.zip", "hunter2", len(entries), nil); err != nil {
		t.Fatalf("a good archive failed its check: %v", err)
	}

	// Where each entry's data starts, asked of the zip library rather than
	// guessed, so the flipped bit lands inside the entry and nowhere else.
	zr, err := zip.OpenReader(filepath.Join(dir, "snap.zip"))
	if err != nil {
		t.Fatal(err)
	}
	offsets := map[string]int64{}
	for _, zf := range zr.File {
		off, err := zf.DataOffset()
		if err != nil {
			t.Fatal(err)
		}
		offsets[zf.Name] = off
	}
	zr.Close()

	for _, e := range entries {
		t.Run(e.name, func(t *testing.T) {
			damaged := append([]byte(nil), good...)
			// Past the salt and the password check bytes, so this is the
			// contents going bad rather than the key: an entry that still
			// opens and no longer holds what was put in it.
			damaged[offsets[e.name]+20] ^= 0x40
			if err := os.WriteFile(filepath.Join(dir, "damaged.zip"), damaged, 0o600); err != nil {
				t.Fatal(err)
			}
			err := verifyZip(b, "damaged.zip", "hunter2", len(entries), nil)
			if err == nil {
				t.Fatal("an archive with a corrupt entry passed its check and would have been kept")
			}
			if !strings.Contains(err.Error(), e.name) {
				t.Errorf("the failure does not say which entry is bad: %v", err)
			}
			assertTheArchiveIsBlamed(t, err)
		})
	}
}

// The password is the whole protection, and a snapshot nobody can open is not a
// backup. Checking that the STORED password opens the archive is the reason the
// check re-reads it at all.
func TestAWrongPasswordIsStillCaught(t *testing.T) {
	dir := t.TempDir()
	stageArchive(t, filepath.Join(dir, "snap.zip"), "correct-horse", zip.AES256Encryption,
		[]testEntry{{"proj/a.txt", []byte("alpha")}, {"proj/b.txt", []byte("beta")}})
	b := localmirror.NewLocalFS(dir)

	err := verifyZip(b, "snap.zip", "hunter2", 2, nil)
	if err == nil {
		t.Fatal("an archive that does not open with the stored password passed its check")
	}
	if !strings.Contains(err.Error(), "password") {
		t.Errorf("the failure does not say the password is wrong: %v", err)
	}
	assertTheArchiveIsBlamed(t, err)

	if err := verifyZip(b, "snap.zip", "correct-horse", 2, nil); err != nil {
		t.Fatalf("the right password was rejected: %v", err)
	}
}

// A SNAPSHOT THAT STOPS PART WAY IS THE FAILURE THAT ACTUALLY HAPPENS: a share
// that drops, a disk that fills, a daemon that is killed. Every cut has to be
// caught, including the one that takes only the index — the archive then holds
// every byte of every file and no tool can find them.
func TestATruncatedArchiveIsStillCaught(t *testing.T) {
	dir := t.TempDir()
	stageArchive(t, filepath.Join(dir, "snap.zip"), "hunter2", zip.AES256Encryption, []testEntry{
		{"proj/a.txt", bytes.Repeat([]byte("alpha "), 2000)},
		{"proj/b.txt", bytes.Repeat([]byte("beta "), 2000)},
		{"proj/c.txt", []byte("gamma")},
	})
	good, err := os.ReadFile(filepath.Join(dir, "snap.zip"))
	if err != nil {
		t.Fatal(err)
	}
	b := localmirror.NewLocalFS(dir)
	index := int64(bytes.Index(good, []byte{'P', 'K', 1, 2}))
	if index <= 0 {
		t.Fatal("the staged archive has no index to cut off")
	}

	for _, tc := range []struct {
		name string
		keep int64
	}{
		{"half the entries", int64(len(good)) / 2},
		{"everything but the index", index},
		{"everything but the last byte of the end record", int64(len(good)) - 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := os.WriteFile(filepath.Join(dir, "cut.zip"), good[:tc.keep], 0o600); err != nil {
				t.Fatal(err)
			}
			err := verifyZip(b, "cut.zip", "hunter2", 3, nil)
			if err == nil {
				t.Fatal("a truncated archive passed its check and would have been kept as a backup")
			}
			assertTheArchiveIsBlamed(t, err)
		})
	}
}

// The end record is the LAST thing a zip contains, so a file that carries on
// past it is not the archive that was written — a write that was retried, two
// snapshots run into one file. Reading to the very last byte is also what makes
// "this archive is complete" true rather than "the first part of it is".
func TestAnArchiveThatDoesNotEndAtItsEndRecordIsCaught(t *testing.T) {
	dir := t.TempDir()
	stageArchive(t, filepath.Join(dir, "snap.zip"), "hunter2", zip.AES256Encryption,
		[]testEntry{{"proj/a.txt", []byte("alpha")}})
	good, err := os.ReadFile(filepath.Join(dir, "snap.zip"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "trailing.zip"), append(good, good...), 0o600); err != nil {
		t.Fatal(err)
	}

	err = verifyZip(localmirror.NewLocalFS(dir), "trailing.zip", "hunter2", 1, nil)
	if err == nil {
		t.Fatal("an archive with another archive stuck on the end of it passed its check")
	}
	assertTheArchiveIsBlamed(t, err)
}

// The index is what every unzip tool reads first, so an archive whose index no
// longer describes what is in it cannot be restored by anything but this
// program's own forward walk.
func TestAnIndexThatDoesNotDescribeTheEntriesIsCaught(t *testing.T) {
	dir := t.TempDir()
	stageArchive(t, filepath.Join(dir, "snap.zip"), "hunter2", zip.AES256Encryption,
		[]testEntry{{"proj/a.txt", []byte("alpha")}, {"proj/b.txt", []byte("beta")}})
	good, err := os.ReadFile(filepath.Join(dir, "snap.zip"))
	if err != nil {
		t.Fatal(err)
	}
	// The LAST copy of the name is the one in the index; the first is in the
	// entry's own header.
	at := bytes.LastIndex(good, []byte("proj/b.txt"))
	if at < 0 {
		t.Fatal("the staged archive does not name its entries where it was expected to")
	}
	good[at+5] = 'X'
	if err := os.WriteFile(filepath.Join(dir, "snap.zip"), good, 0o600); err != nil {
		t.Fatal(err)
	}

	err = verifyZip(localmirror.NewLocalFS(dir), "snap.zip", "hunter2", 2, nil)
	if err == nil {
		t.Fatal("an archive whose index does not match its entries passed its check")
	}
	assertTheArchiveIsBlamed(t, err)
}

// A snapshot of a home directory is 412,415 entries, and a plain zip end record
// counts to 65,535. Every real run therefore ends with the zip64 pair, and the
// check has to walk it — getting this wrong would fail every genuine snapshot
// and delete it.
//
// The fixture is unencrypted and empty, and only the tail is walked: what is
// under test is how the file ENDS, and encrypting seventy thousand entries costs
// a key derivation each, twice, for no extra coverage.
func TestAnArchiveWithMoreEntriesThanAPlainZipCanCountIsStillChecked(t *testing.T) {
	const entries = 70000
	dir := t.TempDir()
	path := filepath.Join(dir, "big.zip")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	bw := bufio.NewWriterSize(f, writeBufferSize)
	zw := zip.NewWriter(bw)
	for i := range entries {
		if _, err := zw.Create(fmt.Sprintf("proj/f%06d.txt", i)); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := bw.Flush(); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	at := bytes.Index(raw, []byte{'P', 'K', 1, 2})
	if at < 0 {
		t.Fatal("the fixture has no index")
	}
	// Everything before the index has been walked by the time readIndex is
	// reached in a real check, and the signature has been read.
	v := &verifier{src: &archiveReader{br: bufio.NewReaderSize(bytes.NewReader(raw[at+4:]), readAheadSize)}}
	records, err := v.readIndex(directoryHeaderSignature, fnv.New64a())
	if err != nil {
		t.Fatalf("an archive of %d entries could not be checked to the end: %v", entries, err)
	}
	if records != entries {
		t.Errorf("the index was read as %d entries, and it holds %d", records, entries)
	}

	// And the count in the end record is believed, not assumed: a zip64 record
	// claiming a different number is a damaged index.
	end := bytes.LastIndex(raw, []byte{'P', 'K', 6, 6})
	if end < 0 {
		t.Fatal("an archive of 70,000 entries was written without a zip64 end record")
	}
	binary.LittleEndian.PutUint64(raw[end+32:], entries-1) // total entries
	v = &verifier{src: &archiveReader{br: bufio.NewReaderSize(bytes.NewReader(raw[at+4:]), readAheadSize)}}
	if _, err := v.readIndex(directoryHeaderSignature, fnv.New64a()); err == nil {
		t.Error("an end record that miscounts the index was accepted")
	}
}

// A file bigger than 4GB — a video, a disk image, the sort of thing a home
// directory holds — makes the pack write an eight-byte descriptor after the
// entry instead of a four-byte one. The walk has to expect the same one, or
// every snapshot holding such a file fails its check and is deleted.
//
// Crafted rather than packed: proving this end to end means pushing 4GB through
// the compressor, which was done once by hand and takes ten seconds. What is
// pinned here is the rule — the same rule the writer uses to choose.
func TestAnEntryBiggerThanFourGigabytesIsMeasuredByItsWideDescriptor(t *testing.T) {
	const big = uint64(4<<30) + 4096
	descriptor := func(csize, usize uint64, wide bool) []byte {
		b := make([]byte, 8, 24)
		binary.LittleEndian.PutUint32(b, dataDescriptorSignature)
		binary.LittleEndian.PutUint32(b[4:], 0) // AE-2 stores no CRC
		if wide {
			b = binary.LittleEndian.AppendUint64(b, csize)
			return binary.LittleEndian.AppendUint64(b, usize)
		}
		b = binary.LittleEndian.AppendUint32(b, uint32(csize))
		return binary.LittleEndian.AppendUint32(b, uint32(usize))
	}
	at := func(b []byte) *verifier {
		return &verifier{
			src:  &archiveReader{br: bufio.NewReaderSize(bytes.NewReader(b), readAheadSize)},
			name: []byte("proj/big.img"),
		}
	}

	if err := at(descriptor(9<<20, big, true)).readDescriptor(9<<20, big); err != nil {
		t.Errorf("an entry of %d bytes was rejected by its own descriptor: %v", big, err)
	}
	if err := at(descriptor(4096, 4096, false)).readDescriptor(4096, 4096); err != nil {
		t.Errorf("an ordinary entry was rejected by its own descriptor: %v", err)
	}
	// And the sizes are held to, not skipped past: an entry that read short is
	// an entry whose data did not all arrive.
	if err := at(descriptor(9<<20, big, true)).readDescriptor(9<<20, big-1); err == nil {
		t.Error("an entry that read one byte short of what it recorded was accepted")
	}
}

// NOTHING OF THE ARCHIVE MAY LAND ON THIS COMPUTER. The check used to copy the
// whole snapshot into /tmp — 57GB of it, on the same disk as the home directory
// it was made from — and the copy survived every kill, because SIGKILL does not
// run the deferred cleanup that removed it. One of them took the root filesystem
// to 90%.
//
// The temp directory is listed DURING the check as well as after it: a file that
// is created and tidied up on the way out would pass an "afterwards" test and
// still be the thing left behind by a process that is killed.
func TestCheckingASnapshotWritesNothingToThisComputer(t *testing.T) {
	cfg, job, b, dst := testSetup(t)
	tmp := t.TempDir() // created BEFORE TMPDIR is redirected, so it is not inside itself
	t.Setenv("TMPDIR", tmp)

	var duringCheck []string
	res := Run(b, cfg, job, "hunter2", slog.New(slog.DiscardHandler), func(p Progress) {
		if p.Phase == PhaseVerifying {
			duringCheck = append(duringCheck, entryNames(tmp)...)
		}
	})
	if res.Err != "" {
		t.Fatalf("the run failed: %s", res.Err)
	}
	if res.Unverified != "" {
		t.Fatalf("the snapshot was not checked, so this proves nothing: %s", res.Unverified)
	}
	if _, err := os.Stat(filepath.Join(dst, filepath.FromSlash(res.File))); err != nil {
		t.Fatalf("no snapshot was written: %v", err)
	}
	if len(duringCheck) != 0 {
		t.Errorf("the check wrote %v to this computer's temporary directory; a killed run would leave it behind", duringCheck)
	}
	if left := entryNames(tmp); len(left) != 0 {
		t.Errorf("the check left %v behind on this computer", left)
	}
}

// entryNames lists a directory, or nothing if it cannot be read.
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

// A DESTINATION THAT STOPS ANSWERING IS NOT A BAD SNAPSHOT. The zip was written
// minutes ago and is complete; a share that drops while it is being read back
// must not cost two and a half hours of packing. It is kept, and it says out
// loud that nothing proved it.
func TestADestinationThatStopsRespondingKeepsTheSnapshotAndSaysItIsUnchecked(t *testing.T) {
	cfg, job, b, dst := testSetup(t)
	dropped := &dropsWhileReadingBack{Backend: b, after: 40}

	res := Run(dropped, cfg, job, "hunter2", slog.New(slog.DiscardHandler), nil)
	if res.Err != "" {
		t.Fatalf("THE RUN FAILED instead of keeping an unchecked snapshot: %s", res.Err)
	}
	if res.File == "" {
		t.Fatal("the snapshot was thrown away because the destination stopped reading back")
	}
	if _, err := os.Stat(filepath.Join(dst, filepath.FromSlash(res.File))); err != nil {
		t.Fatalf("the snapshot is not on the destination: %v", err)
	}
	if res.Unverified == "" {
		t.Fatal("the run claims the snapshot was checked, and the read back never finished")
	}
	if !strings.Contains(res.Unverified, "not checked") || !strings.Contains(res.Unverified, "host is down") {
		t.Errorf("the reason does not say what happened, so nobody can act on it: %s", res.Unverified)
	}
}

// dropsWhileReadingBack is a destination that hands back the first bytes of the
// archive and then fails, the way a share does when the machine holding it goes
// away mid-transfer.
type dropsWhileReadingBack struct {
	localmirror.Backend
	after int
}

func (d *dropsWhileReadingBack) OpenRead(p string) (io.ReadCloser, error) {
	rc, err := d.Backend.OpenRead(p)
	if err != nil {
		return nil, err
	}
	return &failsAfter{ReadCloser: rc, left: d.after}, nil
}

type failsAfter struct {
	io.ReadCloser
	left int
}

func (f *failsAfter) Read(p []byte) (int, error) {
	if f.left <= 0 {
		return 0, errors.New("host is down")
	}
	if len(p) > f.left {
		p = p[:f.left]
	}
	n, err := f.ReadCloser.Read(p)
	f.left -= n
	return n, err
}

// assertTheArchiveIsBlamed keeps the two outcomes apart. A destination that
// stopped answering keeps the snapshot; an archive that is wrong must not,
// because keeping it would mean reporting a backup that cannot be restored as a
// backup that can.
func assertTheArchiveIsBlamed(t *testing.T, err error) {
	t.Helper()
	var unreadable *destUnreadableError
	if errors.As(err, &unreadable) {
		t.Fatalf("a damaged archive was blamed on the destination, so the run would keep it: %v", err)
	}
}
