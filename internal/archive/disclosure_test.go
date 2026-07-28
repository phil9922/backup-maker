// SPDX-License-Identifier: MIT

package archive

import (
	stdzip "archive/zip"
	"log/slog"
	"path/filepath"
	"testing"
)

// WHAT A SNAPSHOT ACTUALLY HIDES — the evidence behind the wording users are
// shown, rather than an assumption about it.
//
// The wizard used to promise that without the password a snapshot "cannot be
// opened — by you or by anyone". That is true of the contents and false of
// everything else: an AES zip encrypts each entry's DATA and leaves the central
// directory — every file name, every size — in the clear. Anyone who picks up
// the drive learns the shape of your whole file tree without knowing the
// password. This project treats a false reassurance as a defect, so the promise
// was corrected, and this test is what keeps the corrected version honest.
//
// It is deliberately read back with the STANDARD LIBRARY's archive/zip, which
// has no concept of a password: that is exactly the position a stranger is in.
//
// If this ever fails because names are no longer readable, something good has
// happened — a move to a format that encrypts the index, like 7z. Update the
// wording in internal/webui/static/index.html, docs/snapshots.md and
// cmd/wizard.go to match, because it will then be understating the protection.
func TestASnapshotHidesItsContentsButNotItsFileNames(t *testing.T) {
	cfg, job, b, dst := testSetup(t)

	res := Run(b, cfg, job, "correct-horse-battery-staple", slog.New(slog.DiscardHandler))
	if res.Err != "" {
		t.Fatalf("run failed: %s", res.Err)
	}
	full := filepath.Join(dst, filepath.FromSlash(res.File))

	zr, err := stdzip.OpenReader(full)
	if err != nil {
		t.Fatalf("a stranger could not even open the container: %v", err)
	}
	defer zr.Close()

	names := map[string]uint64{}
	for _, zf := range zr.File {
		names[zf.Name] = zf.UncompressedSize64
		// The half that IS protected. Every entry must refuse to yield its
		// bytes to a reader holding no password.
		rc, err := zf.Open()
		if err != nil {
			continue // no key, no plaintext — as intended
		}
		buf := make([]byte, 8)
		n, _ := rc.Read(buf)
		rc.Close()
		if n > 0 {
			t.Fatalf("entry %s handed over its contents with no password: %q", zf.Name, buf[:n])
		}
	}

	// The half that is NOT, and that the user is now told about plainly.
	if len(names) == 0 {
		t.Fatal("no entries were listed; the fixture built nothing to reason about")
	}
	if _, ok := names["proj/a.txt"]; !ok {
		t.Errorf("expected the file list to be readable without a password, got %v", names)
	}
	if names["proj/a.txt"] != uint64(len("alpha")) {
		t.Errorf("expected the true size to be readable without a password, got %d", names["proj/a.txt"])
	}
}
