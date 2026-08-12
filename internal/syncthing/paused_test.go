// SPDX-License-Identifier: MIT

package syncthing

import (
	"testing"

	"github.com/phil9922/backup-maker/internal/config"
)

// A paired computer is fed by the sync engine, not by the mirror engines, so
// pausing that pair has to stop the folder being SHARED with that machine.
// Otherwise the copying carries on while every surface says "paused" — the
// dashboard lying about what is happening to somebody's files.
//
// And it must stop exactly that pair: the folder's other paired machines, and
// the machine's other folders, keep going.
func TestAPausedPairIsNotOfferedToThatMachine(t *testing.T) {
	const laptop = "AAAAAAA-BBBBBBB-CCCCCCC"
	const pi = "DDDDDDD-EEEEEEE-FFFFFFF"
	cfg := &config.Config{
		General: config.General{MachineName: "my-laptop"},
		Folders: []config.Folder{
			{ID: "kqz3d-8xh2p", Label: "photos", Path: "/home/alex/photos", PausedTargets: []string{"attic-pi"}},
			{ID: "b7m2p-x91qd", Label: "code", Path: "/home/alex/code"},
		},
		Targets: []config.Target{
			{Type: "device", Name: "spare-laptop", DeviceID: laptop},
			{Type: "device", Name: "attic-pi", DeviceID: pi},
		},
	}

	got := folderDeviceMap(cfg)

	for _, d := range got["kqz3d-8xh2p"] {
		if d.DeviceID == pi {
			t.Error("a paused folder is still shared with the machine it was paused for; " +
				"it would go on being copied there while the dashboard says paused")
		}
	}
	if len(got["kqz3d-8xh2p"]) != 1 || got["kqz3d-8xh2p"][0].DeviceID != laptop {
		t.Errorf("photos is shared with %v, want the machine that was NOT paused", got["kqz3d-8xh2p"])
	}
	if len(got["b7m2p-x91qd"]) != 2 {
		t.Errorf("code is shared with %v, want both machines — pausing one folder must not "+
			"stop another", got["b7m2p-x91qd"])
	}
}
