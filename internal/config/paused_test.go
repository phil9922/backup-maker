// SPDX-License-Identifier: MIT

package config

import "testing"

// THE GUARANTEE, and it is the exact opposite of the one next door.
//
// An empty Target.Folders list means EVERY folder — a convention this project
// has been bitten by three times. Folder.PausedTargets is a second list of names
// living one struct away from it, and its empty case has to mean NOTHING is
// paused. If the two were ever read through one helper, or the field moved onto
// the Target, an empty list would flip from "back everything up" to "back
// nothing up", which is the silent no-backup state the whole program exists to
// prevent.
func TestAnEmptyPausedListMeansNothingIsPausedNotEverything(t *testing.T) {
	cfg := &Config{
		General: General{MachineName: "my-laptop"},
		Folders: []Folder{
			{ID: "kqz3d-8xh2p", Label: "photos", Path: "/home/alex/photos"},
			{ID: "b7m2p-x91qd", Label: "code", Path: "/home/alex/code"},
		},
		Targets: []Target{{Type: "drive", Name: "sdcard", Path: "/media/alex/SDCARD"}},
	}

	// No list at all: nothing is paused anywhere.
	for _, f := range cfg.Folders {
		if cfg.MirrorPaused(f.ID, "sdcard") {
			t.Errorf("%q reads as paused with no paused_targets recorded anywhere", f.Label)
		}
	}
	// A list that exists and is empty — what a resume leaves behind, and what a
	// hand-edited config.toml can hold — means the same thing.
	cfg.Folders[0].PausedTargets = []string{}
	if cfg.MirrorPaused("kqz3d-8xh2p", "sdcard") {
		t.Error("an EMPTY paused list paused a mirror; empty must mean nothing is paused, " +
			"or a dropped entry silently stops backing something up")
	}

	// And a named destination pauses that pair and only that pair.
	cfg.Folders[0].PausedTargets = []string{"sdcard"}
	if !cfg.MirrorPaused("kqz3d-8xh2p", "sdcard") {
		t.Error("a named destination did not pause the pair")
	}
	if cfg.MirrorPaused("b7m2p-x91qd", "sdcard") {
		t.Error("pausing one folder's mirror paused another folder's")
	}
	if cfg.MirrorPaused("kqz3d-8xh2p", "nas") {
		t.Error("pausing one destination paused a different one")
	}
	if cfg.MirrorPaused("no-such-folder", "sdcard") {
		t.Error("an unknown folder reads as paused")
	}
}

// Pausing decides whether an engine RUNS. It must not change which folders a
// destination is configured to hold — that answer is what prunes the pair's
// last-synced clock, what a stop records as a copy sitting on the storage, and
// what tells the dashboard the folder has a continuous backup at all.
func TestPausingAMirrorDoesNotChangeWhatADestinationCovers(t *testing.T) {
	cfg := &Config{
		General: General{MachineName: "my-laptop"},
		Folders: []Folder{
			{ID: "kqz3d-8xh2p", Label: "photos", Path: "/home/alex/photos", PausedTargets: []string{"sdcard"}},
			{ID: "b7m2p-x91qd", Label: "code", Path: "/home/alex/code"},
		},
		Targets: []Target{{Type: "drive", Name: "sdcard", Path: "/media/alex/SDCARD"}},
	}
	got := cfg.FoldersForTarget(cfg.Targets[0])
	if len(got) != 2 {
		t.Fatalf("a paused folder dropped out of the destination's folder list: %v", got)
	}
	if !cfg.Protected("kqz3d-8xh2p") {
		t.Error("a paused folder reads as backed up by nothing; pausing is temporary, " +
			"and the folder still has a mirror configured")
	}
}
