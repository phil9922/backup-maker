// SPDX-License-Identifier: MIT

package setup

import "testing"

// aJob makes a folder, a destination and one snapshot schedule over them.
func aJob(t *testing.T) {
	t.Helper()
	isolate(t)
	base := t.TempDir()
	src := mustDir(t, base, "src")
	dest := mustDir(t, base, "dest")
	folder, targets, err := CreateBackup(BackupRequest{
		Path: src, Destinations: []Destination{{Path: dest}}})
	if err != nil {
		t.Fatal(err)
	}
	if err := AddArchive("nightly", []string{folder.ID}, "daily", targets[0].Name, 3, "pw"); err != nil {
		t.Fatal(err)
	}
}

// THE GUARANTEE: what a snapshot packs can be changed after it exists.
//
// It could not be, and that was the sharpest illustration of the cost of asking
// these questions through browser prompt() boxes: a prompt takes one line of text,
// so the wizard's third question — pack everything, or skip the usual junk? — had
// nowhere to live once setup was over. On one real machine that setting was the
// difference between a 4.3GB nightly zip and a 25GB one (node_modules alone was
// 11GB), and the only route to it was deleting the schedule and retyping a
// password that by design cannot be recovered.
func TestWhatASnapshotPacksCanBeChangedAfterwards(t *testing.T) {
	aJob(t)
	if load(t).Archives[0].NoDefaultIgnores {
		t.Fatal("a new schedule should skip the usual junk by default")
	}

	yes := true
	if err := SetArchiveSchedule("nightly", "", 0, &yes); err != nil {
		t.Fatal(err)
	}
	if !load(t).Archives[0].NoDefaultIgnores {
		t.Error("asking a schedule to pack everything did not take effect")
	}

	no := false
	if err := SetArchiveSchedule("nightly", "", 0, &no); err != nil {
		t.Fatal(err)
	}
	if load(t).Archives[0].NoDefaultIgnores {
		t.Error("asking it to skip junk again did not take effect")
	}
}

// THE GUARANTEE, and the reason the argument is a pointer: editing the interval
// must not quietly change what the snapshot packs.
//
// With a plain bool, every edit made from a form that did not happen to include
// this field would send false and switch a job that was deliberately packing
// everything back to skipping node_modules — turning a 25GB archive into a 4.3GB
// one with no mention of it. Silence has to mean "leave it alone".
func TestEditingTheIntervalLeavesTheContentsSettingAlone(t *testing.T) {
	aJob(t)
	yes := true
	if err := SetArchiveSchedule("nightly", "", 0, &yes); err != nil {
		t.Fatal(err)
	}

	// An edit that says nothing about contents.
	if err := SetArchiveSchedule("nightly", "weekly", 9, nil); err != nil {
		t.Fatal(err)
	}
	a := load(t).Archives[0]
	if !a.NoDefaultIgnores {
		t.Error("changing the interval silently switched the snapshot back to " +
			"skipping junk — the setting must survive an edit that does not mention it")
	}
	if a.Every != "weekly" || a.Keep != 9 {
		t.Errorf("the edit that was asked for did not apply: %+v", a)
	}
}

// A refused edit changes nothing at all, contents included. editArchive validates
// the whole config after applying, so a bad interval must not leave the contents
// flag flipped on its way out.
func TestARefusedEditLeavesTheContentsSettingUntouched(t *testing.T) {
	aJob(t)
	yes := true
	if err := SetArchiveSchedule("nightly", "nonsense", 0, &yes); err == nil {
		t.Fatal("an unusable interval was accepted")
	}
	if load(t).Archives[0].NoDefaultIgnores {
		t.Error("a refused edit still changed what the snapshot packs")
	}
}
