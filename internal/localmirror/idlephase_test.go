// SPDX-License-Identifier: MIT

package localmirror

import (
	"os"
	"path/filepath"
	"testing"
)

// THE GUARANTEE: a pass that has finished is not still described as running.
//
// THE BUG, and it is the one that gets reported. phase was cleared only by
// beginTransfer — that is, only when a pass found something to copy. A pass that
// found nothing left phase on "tidying" indefinitely, and the dashboard narrates
// whatever phase is set: a folder that was completely backed up and completely
// idle sat there reading "checking for deleted files: 72,555" beside an animated
// bar until something happened to change it.
//
// So an idle machine looked simultaneously busy and stuck, which is precisely how
// it gets described: "it hasn't backed up in ten minutes and it's just sitting on
// checking for deleted files". Nothing was wrong. It had nothing to do, and the
// screen was still describing the last thing it had done.
//
// The CLI never had this bug, because it prints a phase only while the state is
// "scanning". The two surfaces disagreeing is what let it survive.
func TestAnIdleEngineIsNotStillDescribingItsLastScan(t *testing.T) {
	src, dst := t.TempDir(), t.TempDir()
	if err := os.WriteFile(filepath.Join(src, "a.txt"), []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	b := NewLocalFS(dst)
	if err := WriteMarker(b, "uuid-1", "mach"); err != nil {
		t.Fatal(err)
	}
	e := New(Options{
		FolderID: "f1", TargetName: "dest", SourcePath: src, Backend: b,
		MachineName: "mach", Label: "proj", UUID: "uuid-1", MaxAgeDays: 30,
		Log: quietLog(),
	})

	// First pass copies the file. Second pass has nothing to do — which is the
	// case that used to leave the phase set, because beginTransfer never ran.
	e.sync()
	e.sync()

	st := e.Status()
	if st.Phase != "" {
		t.Errorf("Status().Phase = %q after an uneventful pass, want empty: the "+
			"dashboard narrates any phase it is given, so a folder with nothing to "+
			"do would go on reporting %q for ever", st.Phase, st.Phase)
	}
	if st.ScannedFiles != 0 || st.ScanTotalFiles != 0 {
		t.Errorf("scan counters left at %d of %d after the pass finished: a count "+
			"on screen describes work that is no longer happening",
			st.ScannedFiles, st.ScanTotalFiles)
	}
	if st.State != "in sync" {
		t.Fatalf("state = %q, want %q — the test is not exercising an idle engine",
			st.State, "in sync")
	}
}

// The same guarantee on the failure path. A destination that vanished mid-listing
// must not keep reporting that it is listing: the row is offline, and an offline
// row narrating progress is the same lie in a worse place.
func TestAPassThatFailedIsNotStillDescribingItsScan(t *testing.T) {
	src := t.TempDir()
	if err := os.WriteFile(filepath.Join(src, "a.txt"), []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	dst := t.TempDir()
	b := NewLocalFS(dst)
	if err := WriteMarker(b, "uuid-1", "mach"); err != nil {
		t.Fatal(err)
	}
	e := New(Options{
		FolderID: "f1", TargetName: "dest", SourcePath: src, Backend: b,
		MachineName: "mach", Label: "proj", UUID: "uuid-1", MaxAgeDays: 30,
		Log: quietLog(),
	})
	e.sync()

	// Take the destination away, then sync again.
	if err := os.RemoveAll(dst); err != nil {
		t.Fatal(err)
	}
	e.sync()

	if st := e.Status(); st.Phase != "" {
		t.Errorf("Status().Phase = %q after a pass that could not complete, want "+
			"empty", st.Phase)
	}
}
