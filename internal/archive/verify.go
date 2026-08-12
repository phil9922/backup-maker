// SPDX-License-Identifier: MIT

package archive

import (
	"bufio"
	"compress/flate"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/pbkdf2"
	"crypto/sha1"
	"crypto/subtle"
	"encoding/binary"
	"fmt"
	"hash"
	"hash/fnv"
	"io"

	"github.com/phil9922/backup-maker/internal/localmirror"
)

// WHY THIS IS A ZIP PARSER AND NOT zip.NewReader.
//
// A zip is INDEXED FROM THE END — the central directory at the tail says where
// every entry is — so opening one needs random access. Verification therefore
// used to copy the finished archive off the destination into a local temp file
// and open that. It cost the archive twice over, once on the destination and
// once on the disk the source folders live on, and opening it built the whole
// central directory in memory: one struct per entry.
//
// A snapshot of a home directory is 412,415 entries and 57GB. On 2026-08-11 the
// daemon was killed by the OOM killer 2½ hours into exactly that run. The
// snapshot was lost, the 57GB spool was left behind in /tmp (SIGKILL does not
// run deferred cleanups) and systemd restarted the daemon straight into an
// identical run — an unbounded loop that would have filled the root filesystem.
//
// So the archive is read ONCE, FORWARDS, off the destination, and none of it is
// kept. A zip's local file headers describe each entry on the way past, and a
// WinZip AES entry's deflate stream says where it ends, so entries are decrypted
// as they are met and thrown away. Memory is one read buffer and one entry's
// worth of scratch, whether the archive holds two files or four hundred
// thousand, and there is no local copy of anything to leak.

// Signatures and fixed lengths from the zip appnote (APPNOTE.TXT, sections
// 4.3.6-4.3.16). Spelled out here because the zip package keeps its own copies
// unexported, and because a forward walk needs them all: this is the only place
// in the program that reads a zip without the index.
const (
	fileHeaderSignature      = 0x04034b50
	directoryHeaderSignature = 0x02014b50
	directoryEndSignature    = 0x06054b50
	directory64LocSignature  = 0x07064b50
	directory64EndSignature  = 0x06064b50
	dataDescriptorSignature  = 0x08074b50

	fileHeaderRest      = 26 // local file header after its signature
	directoryHeaderRest = 42 // central directory record after its signature
	directoryEndRest    = 18 // end-of-central-directory record after its signature
	directory64EndFixed = 44 // zip64 end record after its signature and size field
	directory64LocRest  = 16 // zip64 locator after its signature

	flagEncrypted      = 0x1
	flagDataDescriptor = 0x8

	methodDeflate = 8
	methodAES     = 99 // WinZip AES; the real method is in the 0x9901 extra field

	winzipAESExtraID = 0x9901
	uint32max        = 1<<32 - 1

	// aesIterations is the PBKDF2 count WinZip AES fixes at 1000. It is the
	// reason verification costs real CPU per entry rather than per byte: the key
	// is derived from a fresh salt for every entry, by the writer and again here.
	aesIterations = 1000
)

// readAheadSize is what is buffered from the destination at a time. 1MB for the
// reason the write side uses it: an SMB backend answers one request per Read and
// waits for it, and verification reads the WHOLE archive back.
const readAheadSize = 1 << 20

// decryptChunk is how much of an entry is decrypted at a time. The decompressor
// asks for compressed input a byte at a time, so serving it from a decrypted
// chunk is what keeps the per-byte cost to a bounds check instead of a cipher
// call and an HMAC update.
const decryptChunk = 32 << 10

// destUnreadableError is the DESTINATION failing to hand the archive back — the
// share dropping mid-read, a disk that will not answer — as opposed to anything
// wrong with the archive itself.
//
// Its own type because the outcome is not a failed run. The zip on the
// destination is complete and was written successfully; only the reading back
// was lost, and deleting hours of work because a wifi link blinked would destroy
// a good backup to punish the network. The run keeps the snapshot and reports it
// as written-but-unchecked, which is what Result.Unverified exists to say.
type destUnreadableError struct{ err error }

func (e *destUnreadableError) Error() string {
	return "reading it back off the destination failed: " + e.err.Error()
}

func (e *destUnreadableError) Unwrap() error { return e.err }

// verifyZip proves the finished snapshot on the destination is restorable, by
// reading it back as a stream and decrypting every entry.
//
// WHAT IT PROVES. The archive opens with the stored password (the password
// verification value of every entry is checked against the stored one); every
// entry's data is there and decrypts (each entry is decrypted, authenticated
// against its own HMAC-SHA1 code, and inflated to the end); every entry's sizes
// match what its own data descriptor recorded; the archive holds exactly the
// number of entries the pack wrote; and the index at the end is present, intact
// to the last byte of the file, and lists exactly the same entry names, in the
// same order, as the entries the walk actually found.
//
// WHAT IT NO LONGER PROVES, now that the index is not what the entries are read
// through: that each index record's stored OFFSET and SIZES point at the entry
// it names. A reader that restores through the central directory — which is
// every unzip tool — would find the entry names and the entry data both correct,
// but is trusting offsets this check does not follow. Nothing weaker than
// reading the archive twice, or seeking through it, can close that gap, and both
// are what this function exists to avoid. The old spooled check followed those
// offsets; it is the one thing given up for a check that fits in memory.
//
// wantFiles is the entry count the pack wrote. onRead, if non-nil, is called with
// the number of bytes read back so far, so a check that takes ten minutes over a
// network can show progress instead of looking hung.
func verifyZip(b localmirror.Backend, relPath, password string, wantFiles int, onRead func(int64)) error {
	rc, err := b.OpenRead(relPath)
	if err != nil {
		return &destUnreadableError{err}
	}
	defer rc.Close()

	dest := &destReader{rc: rc, report: onRead}
	v := &verifier{
		src:      &archiveReader{br: bufio.NewReaderSize(dest, readAheadSize)},
		password: password,
		plain:    make([]byte, decryptChunk),
	}
	err = v.run(wantFiles)
	// ASKED AFTER THE FACT, because a destination that stops answering surfaces
	// as whatever the parser made of the bytes it never got. If the destination
	// itself failed, that is what this run is reporting, not a bad archive.
	if dest.err != nil {
		return &destUnreadableError{dest.err}
	}
	return err
}

// destReader is the archive coming off the destination: it counts what has been
// read so verification can report progress, and remembers a failure of the
// destination itself so it is not mistaken for a corrupt archive.
//
// VERIFICATION IS NOT A ROUNDING ERROR AT THE END — it re-reads every byte of
// the archive, which for a 3.4GB snapshot over a network was ten minutes with a
// full bar and a state that still said "running". It looked hung, and was
// reported as hung, while it was doing exactly what it promises.
type destReader struct {
	rc     io.ReadCloser
	n      int64
	report func(int64)
	err    error
}

func (d *destReader) Read(p []byte) (int, error) {
	n, err := d.rc.Read(p)
	d.n += int64(n)
	if d.report != nil {
		d.report(d.n)
	}
	if err != nil && err != io.EOF {
		d.err = err
	}
	return n, err
}

// archiveReader is the archive as a one-way stream, with the position kept so a
// failure can say where in the file it was. Everything above it reads through
// these methods, so the position is always the true offset in the archive —
// which is only true because nothing else is allowed to touch the buffer.
type archiveReader struct {
	br  *bufio.Reader
	pos int64
	// hdr is scratch for one fixed-size record, sized by the largest of them
	// (the zip64 end record) because they share it.
	hdr [directory64EndFixed]byte
}

func (a *archiveReader) full(p []byte) error {
	n, err := io.ReadFull(a.br, p)
	a.pos += int64(n)
	return err
}

// fixed reads the fixed-size body of a record into shared scratch. The result is
// only valid until the next call, which is all any caller here needs and is what
// keeps a 412,415-entry walk from allocating per entry.
func (a *archiveReader) fixed(n int) ([]byte, error) {
	p := a.hdr[:n]
	if err := a.full(p); err != nil {
		return nil, err
	}
	return p, nil
}

func (a *archiveReader) uint32() (uint32, error) {
	p, err := a.fixed(4)
	if err != nil {
		return 0, err
	}
	return binary.LittleEndian.Uint32(p), nil
}

func (a *archiveReader) skip(n int64) error {
	for n > 0 {
		step := n
		if step > readAheadSize {
			step = readAheadSize
		}
		got, err := a.br.Discard(int(step))
		a.pos += int64(got)
		if err != nil {
			return err
		}
		n -= int64(got)
	}
	return nil
}

func (a *archiveReader) peek(n int) ([]byte, error) { return a.br.Peek(n) }

func (a *archiveReader) discard(n int) error {
	got, err := a.br.Discard(n)
	a.pos += int64(got)
	return err
}

// atEnd reports whether the archive really is finished. The end record is the
// last thing a zip contains, so anything after it — or a stream that has not
// ended — means the file is not the archive that was written.
func (a *archiveReader) atEnd() error {
	if _, err := a.br.ReadByte(); err != io.EOF {
		if err != nil {
			return err
		}
		return fmt.Errorf("there are more bytes after the end of the archive at offset %d", a.pos)
	}
	return nil
}

// verifier walks one archive. Everything it needs is allocated once: a
// 412,415-entry snapshot must cost the same as a two-entry one.
type verifier struct {
	src      *archiveReader
	password string
	plain    []byte // one decrypted chunk of the entry being read
	name     []byte // the current entry's name, reused
	extra    []byte // the current entry's extra field, reused
	head     [18]byte
	code     [10]byte
	sum      [sha1.Size]byte
	entry    entryReader
	inflate  io.ReadCloser // reset per entry rather than rebuilt
}

// run walks the whole archive: every entry, then the index that describes them.
func (v *verifier) run(wantFiles int) error {
	// The index is compared by summary rather than by list. Holding 412,415
	// names to check them against the index afterwards is the memory this whole
	// change exists to remove, and a running sum answers the same question:
	// does the index describe THESE entries, in this order?
	walked, indexed := fnv.New64a(), fnv.New64a()

	entries := 0
	sig, err := v.src.uint32()
	if err != nil {
		return fmt.Errorf("the archive could not be read at all: %w", err)
	}
	for sig == fileHeaderSignature {
		if err := v.readEntry(walked); err != nil {
			return err
		}
		entries++
		if sig, err = v.src.uint32(); err != nil {
			return fmt.Errorf("the archive ends after %d entries, with no index: %w", entries, err)
		}
	}
	if entries != wantFiles {
		return fmt.Errorf("archive has %d entries, expected %d", entries, wantFiles)
	}

	records, err := v.readIndex(sig, indexed)
	if err != nil {
		return err
	}
	if records != int64(entries) {
		return fmt.Errorf("the archive's index lists %d entries but the archive holds %d", records, entries)
	}
	if walked.Sum64() != indexed.Sum64() {
		return fmt.Errorf("the archive's index does not describe the %d entries the archive holds", entries)
	}
	return nil
}

// readEntry checks one entry: its header says what this program writes, its data
// decrypts and inflates, and the descriptor that follows it agrees with what was
// actually read. The signature has already been read.
func (v *verifier) readEntry(walked hash.Hash64) error {
	at := v.src.pos - 4
	h, err := v.src.fixed(fileHeaderRest)
	if err != nil {
		return fmt.Errorf("the archive ends inside an entry header at offset %d: %w", at, err)
	}
	flags := binary.LittleEndian.Uint16(h[2:])
	method := binary.LittleEndian.Uint16(h[4:])
	nameLen := int(binary.LittleEndian.Uint16(h[22:]))
	extraLen := int(binary.LittleEndian.Uint16(h[24:]))

	v.name = grow(v.name, nameLen)
	if err := v.src.full(v.name); err != nil {
		return fmt.Errorf("the archive ends inside an entry name at offset %d: %w", at, err)
	}
	walked.Write(v.name)
	walked.Write([]byte{0}) // a name cannot contain NUL, so this cannot run two together
	v.extra = grow(v.extra, extraLen)
	if err := v.src.full(v.extra); err != nil {
		return fmt.Errorf("the archive ends inside the header of %s: %w", v.name, err)
	}

	// AN UNENCRYPTED ENTRY IS A FAILURE, not something to check leniently. The
	// whole promise of a snapshot is that the drive it sits on can be lost, and
	// an entry whose data is in the clear breaks that promise silently.
	if flags&flagEncrypted == 0 {
		return fmt.Errorf("%s is not encrypted", v.name)
	}
	// The pack writes sizes in a trailing descriptor, never in the header, so a
	// header claiming otherwise is not an archive this program wrote — and a
	// forward walk would be reading the wrong bytes if it carried on.
	if flags&flagDataDescriptor == 0 {
		return fmt.Errorf("%s does not carry a data descriptor, so this is not an archive backup-maker wrote", v.name)
	}
	if method != methodAES {
		return fmt.Errorf("%s uses compression method %d, and verification only understands WinZip AES entries", v.name, method)
	}
	strength, inner, err := aesExtra(v.extra)
	if err != nil {
		return fmt.Errorf("%s: %w", v.name, err)
	}
	// Deflate is what writeZip asks for on every entry. It also self-terminates,
	// which is what makes a forward walk possible at all: a stored entry would
	// need a size this program only writes AFTER the data.
	if inner != methodDeflate {
		return fmt.Errorf("%s is stored with method %d inside its encryption, and only deflate can be checked as a stream", v.name, inner)
	}

	csize, usize, err := v.readData(strength)
	if err != nil {
		return fmt.Errorf("cannot decrypt %s: %w", v.name, err)
	}
	return v.readDescriptor(csize, usize)
}

// readData decrypts, authenticates and inflates one entry's data, returning what
// it measured on the way past: the compressed size as it sits in the archive
// (salt, password check, ciphertext and authentication code) and the size the
// entry inflates back to.
func (v *verifier) readData(strength byte) (csize, usize uint64, err error) {
	keyLen := aesKeyLen(strength)
	if keyLen == 0 {
		return 0, 0, fmt.Errorf("unknown AES strength %d", strength)
	}
	saltLen := keyLen / 2
	head := v.head[:saltLen+2]
	if err := v.src.full(head); err != nil {
		return 0, 0, err
	}
	salt, stored := head[:saltLen], head[saltLen:]

	key, err := pbkdf2.Key(sha1.New, v.password, salt, aesIterations, 2*keyLen+2)
	if err != nil {
		return 0, 0, err
	}
	encKey, authKey, check := key[:keyLen], key[keyLen:2*keyLen], key[2*keyLen:]
	// THE PASSWORD CHECK IS THE FIRST THING THE FORMAT OFFERS and the cheapest:
	// two bytes derived alongside the key. It is why a wrong password is caught
	// on the first entry instead of after a decrypted 57GB of noise.
	if subtle.ConstantTimeCompare(check, stored) != 1 {
		return 0, 0, fmt.Errorf("the stored password does not open it")
	}
	block, err := aes.NewCipher(encKey)
	if err != nil {
		return 0, 0, err
	}

	v.entry.reset(v.src, newWinZipCTR(block), hmac.New(sha1.New, authKey), v.plain)
	if v.inflate == nil {
		v.inflate = flate.NewReader(&v.entry)
	} else if err := v.inflate.(flate.Resetter).Reset(&v.entry, nil); err != nil {
		return 0, 0, err
	}
	// io.Discard's ReadFrom serves the copy from a pooled buffer, so decrypting
	// a 20GB entry and a 20-byte one cost the same in memory.
	inflated, err := io.Copy(io.Discard, v.inflate)
	if err != nil {
		return 0, 0, err
	}
	if err := v.entry.settle(); err != nil {
		return 0, 0, err
	}

	// The authentication code covers the ciphertext, so a single flipped bit
	// anywhere in the entry is caught here even when it still inflates.
	code := v.code[:]
	if err := v.src.full(code); err != nil {
		return 0, 0, err
	}
	if !hmac.Equal(v.entry.mac.Sum(v.sum[:0])[:len(code)], code) {
		return 0, 0, fmt.Errorf("its contents do not match their authentication code")
	}
	return uint64(saltLen) + 2 + uint64(v.entry.n) + uint64(len(code)), uint64(inflated), nil
}

// readDescriptor reads the record that follows an entry and holds it to what the
// walk actually measured. It is the entry's own account of itself, written after
// its data, and it is the only place those sizes appear that a forward reader
// can reach.
func (v *verifier) readDescriptor(csize, usize uint64) error {
	sig, err := v.src.uint32()
	if err != nil {
		return fmt.Errorf("the archive ends where %s's descriptor should be: %w", v.name, err)
	}
	if sig != dataDescriptorSignature {
		return fmt.Errorf("%s is not followed by a descriptor, so its contents did not end where the archive says", v.name)
	}
	// The CRC is deliberately not read for anything: WinZip AE-2 stores no CRC,
	// so the pack writes a zero here and the authentication code above is what
	// stands in its place.
	wide := csize > uint32max || usize > uint32max // the rule the writer uses
	n := 12
	if wide {
		n = 20
	}
	d, err := v.src.fixed(n)
	if err != nil {
		return fmt.Errorf("the archive ends inside %s's descriptor: %w", v.name, err)
	}
	var gotC, gotU uint64
	if wide {
		gotC, gotU = binary.LittleEndian.Uint64(d[4:]), binary.LittleEndian.Uint64(d[12:])
	} else {
		gotC, gotU = uint64(binary.LittleEndian.Uint32(d[4:])), uint64(binary.LittleEndian.Uint32(d[8:]))
	}
	if gotC != csize || gotU != usize {
		return fmt.Errorf("%s says it holds %d bytes compressed and %d uncompressed, and %d and %d were read",
			v.name, gotC, gotU, csize, usize)
	}
	return nil
}

// readIndex walks the central directory and the end record after it, and returns
// how many entries the index claims. sig is the signature already read.
//
// The records are summed into indexed, not stored: see run. Their offsets and
// sizes are skipped rather than checked — following them is what needs random
// access, and what this check gives up.
func (v *verifier) readIndex(sig uint32, indexed hash.Hash64) (int64, error) {
	records := int64(0)
	var err error
	for sig == directoryHeaderSignature {
		h, ferr := v.src.fixed(directoryHeaderRest)
		if ferr != nil {
			return 0, fmt.Errorf("the archive ends inside its index, %d entries in: %w", records, ferr)
		}
		nameLen := int(binary.LittleEndian.Uint16(h[24:]))
		extraLen := int(binary.LittleEndian.Uint16(h[26:]))
		commentLen := int(binary.LittleEndian.Uint16(h[28:]))
		v.name = grow(v.name, nameLen)
		if err := v.src.full(v.name); err != nil {
			return 0, fmt.Errorf("the archive ends inside its index, %d entries in: %w", records, err)
		}
		indexed.Write(v.name)
		indexed.Write([]byte{0})
		if err := v.src.skip(int64(extraLen + commentLen)); err != nil {
			return 0, fmt.Errorf("the archive ends inside its index, %d entries in: %w", records, err)
		}
		records++
		if sig, err = v.src.uint32(); err != nil {
			return 0, fmt.Errorf("the archive ends after %d index entries, with no end record: %w", records, err)
		}
	}

	// THE END RECORD IS THE LAST THING IN THE FILE, so reaching it is what
	// proves the archive on the destination is complete and not a snapshot that
	// stopped part way. An archive of more than 65,535 entries — which a
	// snapshot of a home directory is, several times over — carries the zip64
	// pair first, with the real count, and leaves markers in the plain record.
	claimed := int64(-1)
	if sig == directory64EndSignature {
		z, err := v.src.fixed(8)
		if err != nil {
			return 0, fmt.Errorf("the archive ends inside its zip64 end record: %w", err)
		}
		size := binary.LittleEndian.Uint64(z)
		if size < directory64EndFixed {
			return 0, fmt.Errorf("the archive's zip64 end record is %d bytes, too short to be one", size)
		}
		e, err := v.src.fixed(directory64EndFixed)
		if err != nil {
			return 0, fmt.Errorf("the archive ends inside its zip64 end record: %w", err)
		}
		claimed = int64(binary.LittleEndian.Uint64(e[20:]))
		if err := v.src.skip(int64(size - directory64EndFixed)); err != nil {
			return 0, fmt.Errorf("the archive ends inside its zip64 end record: %w", err)
		}
		if sig, err = v.src.uint32(); err != nil {
			return 0, fmt.Errorf("the archive ends before its zip64 locator: %w", err)
		}
		if sig != directory64LocSignature {
			return 0, fmt.Errorf("the archive's zip64 end record is followed by %#08x, not its locator", sig)
		}
		if err := v.src.skip(directory64LocRest); err != nil {
			return 0, fmt.Errorf("the archive ends inside its zip64 locator: %w", err)
		}
		if sig, err = v.src.uint32(); err != nil {
			return 0, fmt.Errorf("the archive ends before its end record: %w", err)
		}
	}
	if sig != directoryEndSignature {
		return 0, fmt.Errorf("the archive has no end record: it reads %#08x at offset %d", sig, v.src.pos-4)
	}
	e, err := v.src.fixed(directoryEndRest)
	if err != nil {
		return 0, fmt.Errorf("the archive ends inside its end record: %w", err)
	}
	if claimed < 0 {
		claimed = int64(binary.LittleEndian.Uint16(e[6:]))
	}
	if err := v.src.skip(int64(binary.LittleEndian.Uint16(e[16:]))); err != nil {
		return 0, fmt.Errorf("the archive ends inside its trailing comment: %w", err)
	}
	if err := v.src.atEnd(); err != nil {
		return 0, err
	}
	if claimed != records {
		return 0, fmt.Errorf("the archive's end record counts %d entries and its index holds %d", claimed, records)
	}
	return records, nil
}

// grow returns p resized to n, reusing its memory when it is already big enough.
func grow(p []byte, n int) []byte {
	if cap(p) >= n {
		return p[:n]
	}
	return make([]byte, n)
}

// aesExtra reads the WinZip AES field (0x9901) out of an entry's extra data: the
// key strength, and the compression method that was applied before encryption.
func aesExtra(extra []byte) (strength byte, method uint16, err error) {
	for len(extra) >= 4 {
		id := binary.LittleEndian.Uint16(extra)
		size := int(binary.LittleEndian.Uint16(extra[2:]))
		if len(extra) < 4+size {
			break
		}
		body := extra[4 : 4+size]
		if id == winzipAESExtraID {
			if size < 7 {
				return 0, 0, fmt.Errorf("its AES header is %d bytes, too short to read", size)
			}
			return body[4], binary.LittleEndian.Uint16(body[5:]), nil
		}
		extra = extra[4+size:]
	}
	return 0, 0, fmt.Errorf("it has no AES header, so it was not written by this program")
}

// aesKeyLen is the key size WinZip AES's strength byte stands for.
func aesKeyLen(strength byte) int {
	switch strength {
	case 1:
		return 16
	case 2:
		return 24
	case 3:
		return 32
	}
	return 0
}

// entryReader hands one entry's deflate stream to the decompressor, decrypting
// as it goes, and never moves the archive past a byte the decompressor did not
// take.
//
// THAT LAST PART IS THE WHOLE TRICK. The decompressor asks for input a byte at a
// time and stops at the end of the deflate stream, which is how a forward walk
// learns where an entry's data ends — nothing else in the file says. So bytes
// are PEEKED from the buffer, decrypted in a chunk for speed, and only marked
// as read (and fed to the authentication code) once the decompressor has
// actually taken them. Whatever was decrypted beyond that belongs to the
// authentication code and the descriptor that follow, and is still sitting in
// the buffer to be read as itself.
type entryReader struct {
	src   *archiveReader
	ctr   *winZipCTR
	mac   hash.Hash
	raw   []byte // ciphertext peeked from src, valid until it is discarded
	plain []byte // the same bytes decrypted
	buf   []byte // plain's storage, reused for every entry
	off   int    // how much of plain the decompressor has taken
	n     int64  // ciphertext the decompressor has taken in total
}

func (e *entryReader) reset(src *archiveReader, ctr *winZipCTR, mac hash.Hash, buf []byte) {
	*e = entryReader{src: src, ctr: ctr, mac: mac, buf: buf}
}

func (e *entryReader) Read(p []byte) (int, error) {
	if e.off == len(e.plain) {
		if err := e.fill(); err != nil {
			return 0, err
		}
	}
	n := copy(p, e.plain[e.off:])
	e.off += n
	return n, nil
}

// ReadByte is why this type implements io.ByteReader at all: without it the
// decompressor wraps the reader in a buffer of its own, which would read ahead
// past the end of the entry and lose the position this walk depends on.
func (e *entryReader) ReadByte() (byte, error) {
	if e.off == len(e.plain) {
		if err := e.fill(); err != nil {
			return 0, err
		}
	}
	c := e.plain[e.off]
	e.off++
	return c, nil
}

// fill decrypts the next chunk. It is only ever reached with the current one
// used up, which is what keeps the keystream in step with the archive: the
// cipher has been advanced over exactly the bytes that have been consumed.
func (e *entryReader) fill() error {
	if err := e.settle(); err != nil {
		return err
	}
	raw, err := e.src.peek(decryptChunk)
	if len(raw) == 0 {
		if err == nil {
			err = io.EOF
		}
		return err
	}
	e.raw = raw
	e.plain = e.buf[:len(raw)]
	e.ctr.xor(e.plain, raw)
	e.off = 0
	return nil
}

// settle moves the archive past what the decompressor took, and authenticates
// exactly those bytes. Called before every refill and once when the entry ends.
func (e *entryReader) settle() error {
	taken, raw := e.off, e.raw
	e.raw, e.plain, e.off = nil, nil, 0
	if taken == 0 {
		return nil
	}
	e.mac.Write(raw[:taken])
	e.n += int64(taken)
	return e.src.discard(taken)
}

// winZipCTR is AES in counter mode as WinZip AES uses it: a 128-bit counter,
// LITTLE-endian, starting at 1. crypto/cipher's NewCTR is big-endian per NIST SP
// 800-38A and cannot produce this keystream, which is why it is spelled out.
type winZipCTR struct {
	block cipher.Block
	ctr   [aes.BlockSize]byte
	ks    [aes.BlockSize]byte
	used  int
}

func newWinZipCTR(block cipher.Block) *winZipCTR {
	c := &winZipCTR{block: block, used: aes.BlockSize}
	c.ctr[0] = 1
	return c
}

// xor decrypts src into dst. Encryption and decryption are the same operation in
// counter mode, which is also why the writer and this share a keystream.
func (c *winZipCTR) xor(dst, src []byte) {
	for len(src) > 0 {
		if c.used == len(c.ks) {
			c.block.Encrypt(c.ks[:], c.ctr[:])
			for i := range c.ctr {
				c.ctr[i]++
				if c.ctr[i] != 0 {
					break
				}
			}
			c.used = 0
		}
		n := subtle.XORBytes(dst, src, c.ks[c.used:])
		c.used += n
		dst, src = dst[n:], src[n:]
	}
}
