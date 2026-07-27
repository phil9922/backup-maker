// SPDX-License-Identifier: MIT

package daemon

import (
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/phil9922/backup-maker/internal/archive"
	"github.com/phil9922/backup-maker/internal/config"
)

// isolateState points the config directory at a temp dir, so a test that
// exercises the real state.json never touches the developer's own.
func isolateState(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)                       // linux
	t.Setenv("HOME", filepath.Join(dir, "home"))           // macOS
	t.Setenv("AppData", filepath.Join(dir, "appdata"))     // windows
	t.Setenv("LOCALAPPDATA", filepath.Join(dir, "local2")) // windows, belt and braces
}

// THE HEADLINE: the odometer is a lifetime figure, so it has to survive the
// daemon being stopped and started. Written through the real state.json, since
// that round trip is the thing that would break.
func TestTallySurvivesARestart(t *testing.T) {
	isolateState(t)

	first := &daemon{state: &config.State{}}
	first.tally = newTally(first.state, first.saveState)
	first.countCopied(1200)
	first.countCopied(800)
	first.tally.flush()

	// A new process reads what the old one left behind.
	reloaded, err := config.LoadState()
	if err != nil {
		t.Fatalf("loading state: %v", err)
	}
	if reloaded.BytesCopiedTotal != 2000 || reloaded.FilesCopiedTotal != 2 {
		t.Fatalf("state.json holds %dB / %d files, want 2000B / 2",
			reloaded.BytesCopiedTotal, reloaded.FilesCopiedTotal)
	}
	if reloaded.CountingSince.IsZero() {
		t.Fatal("no start date was recorded, so the UI cannot say since when")
	}

	second := &daemon{state: reloaded}
	second.tally = newTally(reloaded, second.saveState)

	// Counting continues from where it stopped rather than starting over.
	bytes, files, since := second.totals()
	if bytes != 2000 || files != 2 {
		t.Fatalf("after restart the odometer reads %dB / %d files, want 2000B / 2", bytes, files)
	}
	if !since.Equal(reloaded.CountingSince) {
		t.Errorf("the start date moved on restart: %v vs %v", since, reloaded.CountingSince)
	}

	second.countCopied(500)
	second.tally.flush()
	after, err := config.LoadState()
	if err != nil {
		t.Fatalf("loading state: %v", err)
	}
	if after.BytesCopiedTotal != 2500 || after.FilesCopiedTotal != 3 {
		t.Fatalf("second run persisted %dB / %d files, want 2500B / 3",
			after.BytesCopiedTotal, after.FilesCopiedTotal)
	}
	if !after.CountingSince.Equal(reloaded.CountingSince) {
		t.Errorf("the start date was reset by the second run: %v", after.CountingSince)
	}
}

// The start date is stamped when counting begins, not back-dated: "since 3
// March" has to be the day the figure actually started accumulating.
func TestTallyStampsTheStartDateOnce(t *testing.T) {
	before := time.Now()
	fresh := newTally(&config.State{}, func(uint64, uint64, time.Time) error { return nil })
	_, _, since := fresh.snapshot()
	if since.Before(before) || since.After(time.Now()) {
		t.Fatalf("a fresh counter stamped %v, which is not now", since)
	}

	stamped := time.Date(2026, 3, 3, 9, 0, 0, 0, time.UTC)
	existing := newTally(&config.State{CountingSince: stamped}, nil)
	if _, _, got := existing.snapshot(); !got.Equal(stamped) {
		t.Fatalf("an existing start date was overwritten: %v", got)
	}
}

// Copying must not rewrite state.json per file: a first sync of a large folder
// would otherwise hammer the disk with thousands of identical writes.
func TestTallyBatchesItsWrites(t *testing.T) {
	writes := 0
	tl := newTally(&config.State{CountingSince: time.Now()},
		func(uint64, uint64, time.Time) error { writes++; return nil })

	for range 500 {
		tl.add(1, 1024)
	}
	if writes != 0 {
		t.Fatalf("500 copied files caused %d writes to state.json before any flush", writes)
	}
	tl.flush()
	if writes != 1 {
		t.Fatalf("flush wrote %d times, want 1", writes)
	}
	// Nothing new to say, so nothing is written.
	tl.flush()
	if writes != 1 {
		t.Fatalf("an idle flush rewrote state.json (%d writes)", writes)
	}
	if bytes, files, _ := tl.snapshot(); bytes != 500*1024 || files != 500 {
		t.Fatalf("counter reads %dB / %d files after 500 copies", bytes, files)
	}
}

// A write that fails must not lose the interval's copying: the next flush
// retries rather than the counter silently regressing.
func TestTallyRetriesAfterAFailedWrite(t *testing.T) {
	fail := true
	var lastBytes uint64
	tl := newTally(&config.State{CountingSince: time.Now()},
		func(b, _ uint64, _ time.Time) error {
			if fail {
				return errors.New("disk full")
			}
			lastBytes = b
			return nil
		})

	tl.add(1, 4096)
	tl.flush()
	fail = false
	tl.flush()
	if lastBytes != 4096 {
		t.Fatalf("after the failed write the retry persisted %dB, want 4096", lastBytes)
	}
}

// A snapshot counts as one file of the size it occupies on the destination.
// Counting its thousands of entries and their uncompressed size would claim
// more data was written than the destination ever received.
func TestArchiveCountsWhatLandedNotWhatWasRead(t *testing.T) {
	isolateState(t)
	d := &daemon{state: &config.State{}}
	d.tally = newTally(d.state, d.saveState)

	d.recordArchiveResult(archive.Result{
		ArchiveName: "nightly",
		When:        time.Now(),
		Files:       9000,     // entries read from the source
		Bytes:       12 << 30, // their uncompressed total
		StoredBytes: 3 << 30,  // what the zip actually occupies
	}, true)

	bytes, files, _ := d.totals()
	if bytes != 3<<30 {
		t.Errorf("odometer took %dB from the snapshot, want the %dB that landed", bytes, 3<<30)
	}
	if files != 1 {
		t.Errorf("odometer took %d files from one snapshot, want 1", files)
	}

	// A failed run leaves nothing on the destination, so it adds nothing.
	d.recordArchiveResult(archive.Result{
		ArchiveName: "nightly", When: time.Now(), Err: "target offline",
	}, false)
	if bytes, files, _ := d.totals(); bytes != 3<<30 || files != 1 {
		t.Errorf("a failed snapshot moved the odometer to %dB / %d files", bytes, files)
	}
}
