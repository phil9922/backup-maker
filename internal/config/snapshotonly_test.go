// SPDX-License-Identifier: MIT

package config

import "testing"

// THE GUARANTEE: asking for a scheduled snapshot of a folder never starts
// copying that folder continuously as well.
//
// This is the regression that produced it. An unscoped destination — one with
// no folder list, meaning "every folder" — is the normal case: it is what you
// get by backing up your first folder and never thinking about scope again.
// Adding a folder for a daily zip then handed it to every one of those
// destinations, which began mirroring it within seconds. On a real machine it
// copied a folder that CONTAINED two folders already being backed up, so
// everything was duplicated, nested, onto every drive, and nothing on screen
// said it was happening.
func TestATimedOnlyFolderIsNeverMirroredByAnEveryFolderTarget(t *testing.T) {
	cfg := &Config{
		Folders: []Folder{
			{ID: "f1", Label: "Development"},
			{ID: "f2", Label: "Desktop", SnapshotOnly: true},
		},
		Targets: []Target{
			{Name: "laptopcard", Type: "drive"}, // no Folders → every folder
			{Name: "backup-pi", Type: "share"},  // no Folders → every folder
		},
	}

	for _, target := range cfg.Targets {
		got := cfg.FoldersForTarget(target)
		for _, f := range got {
			if f.ID == "f2" {
				t.Errorf("%s mirrors %q, which was set up for scheduled snapshots only", target.Name, f.Label)
			}
		}
		if len(got) != 1 || got[0].ID != "f1" {
			t.Errorf("%s mirrors %v, want only the folder that asked for a mirror", target.Name, ids(got))
		}
	}
}

// The flag must not break deliberate scoping: naming a folder explicitly is an
// instruction, and it is the supported way to give a snapshot-only folder a
// mirror on one chosen destination without it becoming every destination's
// business.
func TestAFolderNamedExplicitlyByATargetIsStillMirrored(t *testing.T) {
	cfg := &Config{
		Folders: []Folder{{ID: "f1", Label: "Desktop", SnapshotOnly: true}},
		Targets: []Target{{Name: "laptopcard", Type: "drive", Folders: []string{"f1"}}},
	}

	got := cfg.FoldersForTarget(cfg.Targets[0])
	if len(got) != 1 || got[0].ID != "f1" {
		t.Fatalf("mirrors %v, want the folder it was explicitly told to mirror", ids(got))
	}
}

// A destination that takes snapshots and nothing else still mirrors nothing —
// the flag on the folder must not have disturbed the flag on the target.
func TestAnArchivesOnlyTargetStillMirrorsNothing(t *testing.T) {
	cfg := &Config{
		Folders: []Folder{{ID: "f1"}, {ID: "f2", SnapshotOnly: true}},
		Targets: []Target{{Name: "pi", Type: "share", ArchivesOnly: true}},
	}

	if got := cfg.FoldersForTarget(cfg.Targets[0]); len(got) != 0 {
		t.Fatalf("an archives-only destination mirrors %v, want nothing", ids(got))
	}
}

// Ordinary folders are unaffected: the common case has to stay the common case.
func TestAnEveryFolderTargetStillMirrorsEveryOrdinaryFolder(t *testing.T) {
	cfg := &Config{
		Folders: []Folder{{ID: "f1"}, {ID: "f2"}, {ID: "f3"}},
		Targets: []Target{{Name: "card", Type: "drive"}},
	}

	if got := cfg.FoldersForTarget(cfg.Targets[0]); len(got) != 3 {
		t.Fatalf("mirrors %v, want all three", ids(got))
	}
}

func ids(fs []Folder) []string {
	out := make([]string, 0, len(fs))
	for _, f := range fs {
		out = append(out, f.ID)
	}
	return out
}
