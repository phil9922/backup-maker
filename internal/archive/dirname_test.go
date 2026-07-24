// SPDX-License-Identifier: MIT

package archive

import (
	"testing"

	"github.com/phil9922/backup-maker/internal/localmirror"
)

// The reclaimer has to know where snapshots live, but internal/archive already
// depends on internal/localmirror, so the name is duplicated there rather than
// imported. If these ever diverge the reclaimer would quietly stop finding
// snapshots — and silently keep a destination full.
func TestArchiveDirNameMatchesReclaimer(t *testing.T) {
	if DirName != localmirror.ArchivesDirName {
		t.Fatalf("archive.DirName = %q but localmirror.ArchivesDirName = %q; "+
			"the reclaimer would no longer find snapshots", DirName, localmirror.ArchivesDirName)
	}
}
