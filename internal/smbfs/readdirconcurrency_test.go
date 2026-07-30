// SPDX-License-Identifier: MIT

package smbfs

import (
	"fmt"
	"os"
	"sync"
	"testing"
	"time"
)

// How much faster a share answers directory listings when more are in flight.
//
// WHY THIS EXISTS. The mirror builds its index of the destination by listing
// every directory, and on a share that is where a pass spends its time: measured
// on a real Pi, 25.7s of a 27.1s pass. The tree is 9,402 directories over 17
// levels, so at eight listings at a time the pass waits on roughly 1,180
// sequential round trips — which works out at about 22ms each, and matches the
// measurement almost exactly.
//
// dirListWorkers was set to eight with the reasoning that "higher would mostly
// buy contention". That was an assumption, and the arithmetic above suggests it is
// wrong. This measures it instead of arguing about it, because the answer depends
// on the server, the link and go-smb2's credit accounting — none of which can be
// reasoned about from here.
//
// Skipped unless BM_SMB_TEST_URL (+_USER/_PASS) is set, like the other tests here.
// It also wants BM_SMB_TEST_DIR pointing at a directory with a real tree under it,
// since listing an empty share measures nothing:
//
//	BM_SMB_TEST_URL=//192.168.1.50/backups BM_SMB_TEST_USER=u BM_SMB_TEST_PASS=p \
//	BM_SMB_TEST_DIR=my-laptop go test ./internal/smbfs -run Concurrency -v
func TestReadDirConcurrencyScaling(t *testing.T) {
	url := os.Getenv("BM_SMB_TEST_URL")
	if url == "" {
		t.Skip("BM_SMB_TEST_URL not set")
	}
	root := os.Getenv("BM_SMB_TEST_DIR")
	if root == "" {
		t.Skip("BM_SMB_TEST_DIR not set: listing an empty share measures nothing")
	}
	fs, err := New(url, os.Getenv("BM_SMB_TEST_USER"), os.Getenv("BM_SMB_TEST_PASS"))
	if err != nil {
		t.Fatalf("connecting to %s: %v", url, err)
	}
	defer fs.Close()

	// Collect the directories once, sequentially, so every concurrency level below
	// walks exactly the same list and the comparison is honest.
	dirs := []string{root}
	for i := 0; i < len(dirs); i++ {
		ents, err := fs.ReadDir(dirs[i])
		if err != nil {
			continue
		}
		for _, d := range ents {
			if d.IsDir() {
				dirs = append(dirs, dirs[i]+"/"+d.Name())
			}
		}
		if len(dirs) > 4000 {
			break // enough to measure with; keeps the test to a sane length
		}
	}
	if len(dirs) < 50 {
		t.Skipf("only %d directories under %s: too few to measure scaling", len(dirs), root)
	}
	t.Logf("listing %d directories at each concurrency level", len(dirs))

	var base time.Duration
	for _, workers := range []int{1, 4, 8, 12, 16, 24, 32} {
		took, failed := timeListings(fs, dirs, workers)
		perDir := took / time.Duration(len(dirs))
		line := fmt.Sprintf("workers=%-3d %8s  %7s/dir  failed=%-5d", workers,
			took.Round(time.Millisecond), perDir.Round(10*time.Microsecond), failed)
		if workers == 1 {
			base = took
		} else if base > 0 {
			line += fmt.Sprintf("%.1fx", float64(base)/float64(took))
		}
		if failed > 0 {
			line += "  ← UNUSABLE: the server refused listings at this level"
		}
		t.Log(line)
	}
	t.Log("dirListWorkers in internal/localmirror/destindex.go must come from the " +
		"fastest level with failed=0, WITH MARGIN — the destination is somebody's " +
		"backup and a refused listing fails the whole pass.")
}

// timeListings reports how long the listings took AND how many of them failed.
//
// COUNTING THE FAILURES IS THE WHOLE POINT, and the first version of this did not.
// It discarded the error from ReadDir, so when a concurrency level was high enough
// for the server to start refusing, the refusals came back fast and were timed as
// though they were successes: the benchmark reported 32 workers as "17x faster"
// when a third of the calls were failing. That number went into the engine, and
// the next pass to the real destination died with "readdir ..." — backups to that
// machine stopped until it was put back.
//
// A benchmark that ignores errors measures the wrong thing in the most flattering
// possible direction. Never let one report a rate without a success count beside it.
func timeListings(fs *FS, dirs []string, workers int) (time.Duration, int) {
	start := time.Now()
	sem := make(chan struct{}, workers)
	var wg sync.WaitGroup
	var mu sync.Mutex
	failed := 0
	for _, d := range dirs {
		wg.Add(1)
		sem <- struct{}{}
		go func(d string) {
			defer wg.Done()
			defer func() { <-sem }()
			if _, err := fs.ReadDir(d); err != nil {
				mu.Lock()
				failed++
				mu.Unlock()
			}
		}(d)
	}
	wg.Wait()
	return time.Since(start), failed
}
