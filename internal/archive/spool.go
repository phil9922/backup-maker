// SPDX-License-Identifier: MIT

package archive

import (
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/phil9922/backup-maker/internal/config"
	"github.com/phil9922/backup-maker/internal/diskspace"
)

// THE PROBLEM THIS FILE EXISTS FOR. Verification proves a finished snapshot is
// restorable by reading it back and decrypting every entry — and a zip is read
// through its central directory, which needs random access, so the archive is
// spooled to a LOCAL temp file first. All of it. A 57GB snapshot spooled into
// /tmp is 57GB written to the OS partition, which on an ordinary Linux install
// is the same disk as the home directory the snapshot was made from. Nothing
// checked that disk had the room.
//
// Filling it is worse than any backup failure this program can have: the user
// loses the machine they are sitting at, and the source data is on that same
// disk. So the room is asked for BEFORE PACKING STARTS — packing 57GB over wifi
// took two hours, and discovering at the end that the check cannot run would
// throw all of it away — and if it is not there, the snapshot is still written
// and verification is skipped, loudly. A written but unchecked snapshot is worth
// far more than no snapshot.

// spoolCheckBytes is how often the local disk is re-measured while the archive
// is being spooled. 64MB against a 2GB floor: the disk cannot go from "fine" to
// full inside one interval even with something else on the machine writing hard,
// and it is 900 statfs calls for a 57GB archive — nothing beside reading 57GB
// off a network share.
const spoolCheckBytes = 64 << 20

// localUsage reports free space on a local path. It is diskspace.Usage, behind
// a variable so tests can present a nearly-full disk without needing one —
// there is no backend interface to wrap here the way a destination has one.
// Nothing outside tests ever assigns it.
var localUsage = diskspace.Usage

// spoolFullError is the local disk running short WHILE the archive is being read
// back. Its own type because the outcome is not a failed run: the snapshot on
// the destination is finished and good, so the spool is thrown away, the archive
// is KEPT, and the run reports itself as written-but-unchecked — exactly what a
// pre-flight shortfall does.
type spoolFullError struct {
	dir  string
	free uint64
}

func (e *spoolFullError) Error() string {
	return fmt.Sprintf("checking it filled the disk holding %s (%s left), so it was stopped before it did",
		e.dir, gb(e.free))
}

// resolveSpoolDir picks the directory the archive is spooled into, and refuses a
// configured one that cannot safely hold it.
//
// EVERY CHECK HERE IS ON HAND-EDITED INPUT. config.toml is a file people open,
// and this value names a directory that a multi-gigabyte file is about to be
// written into. A relative path resolves against whatever the daemon's working
// directory happens to be; a missing one may be an unplugged disk; a path inside
// a backed-up folder puts the spool into the very tree being snapshotted, where
// the next pass would copy it to every destination configured.
//
// REFUSING MEANS NOT VERIFYING, NOT FALLING BACK. Quietly using /tmp instead is
// how somebody who moved the spool off their OS disk on purpose ends up filling
// it anyway, having been told it was fixed.
func resolveSpoolDir(cfg *config.Config) (string, error) {
	dir := strings.TrimSpace(cfg.Defaults.VerifySpoolDir)
	if dir == "" {
		// os.TempDir honours TMPDIR, which is worth something on a machine where
		// somebody exports it — but the daemon is started by systemd with no
		// TMPDIR at all, so on the machine that matters this is /tmp.
		return os.TempDir(), nil
	}
	if !filepath.IsAbs(dir) {
		return "", fmt.Errorf("verify_spool_dir must be an absolute path, and %q is not", dir)
	}
	dir = filepath.Clean(dir)

	// ASKED BEFORE THE DIRECTORY IS TOUCHED, including before the write probe
	// below: the probe creates a file, and creating one inside a folder being
	// backed up is the thing this check exists to prevent.
	for _, f := range cfg.Folders {
		if config.PathWithin(f.Path, dir) {
			return "", fmt.Errorf("verify_spool_dir %s is inside %s, which is being backed up: "+
				"a snapshot spooled there would be packed into the next one and copied to every destination", dir, f.Path)
		}
	}

	info, err := os.Stat(dir)
	if err != nil {
		return "", fmt.Errorf("verify_spool_dir %s cannot be used: %w", dir, err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("verify_spool_dir %s is not a directory", dir)
	}
	// Writable is asked by writing: the permission bits do not answer it on a
	// read-only mount, and finding out later means finding out after two hours
	// of packing.
	probe, err := os.CreateTemp(dir, "backup-maker-spool-probe-*")
	if err != nil {
		return "", fmt.Errorf("verify_spool_dir %s cannot be written to: %w", dir, err)
	}
	name := probe.Name()
	probe.Close()
	// A file this process created a line ago, in a directory proved above not to
	// be inside any folder being backed up. Nothing else here is ever deleted.
	_ = os.Remove(name)
	return dir, nil
}

// planSpool decides, BEFORE ANY PACKING, where the finished snapshot will be
// spooled for verification — or why it cannot be verified at all.
//
// A non-empty second result is the reason to carry on the run's Result: the
// snapshot is still written, and it says in one sentence why it was not checked.
// need is the same estimate the destination pre-flight uses; the spool holds
// exactly the archive that lands on the destination, so it is the right figure.
func planSpool(cfg *config.Config, job config.Archive, need uint64, log *slog.Logger) (dir, unverified string) {
	dir, err := resolveSpoolDir(cfg)
	if err != nil {
		log.Warn("this snapshot will be written but NOT checked: the verification spool directory cannot be used",
			"archive", job.Name, "err", err)
		return "", "the snapshot was written but not checked: " + err.Error()
	}
	avail, _, err := localUsage(dir)
	if err != nil {
		// Same reading ensureRoomFor gives an unmeasurable destination: behave as
		// this program did before the check existed. The guard that runs DURING
		// the spool still stops the disk being driven to zero.
		log.Warn("could not read free space where snapshots are spooled for checking; checking it anyway",
			"archive", job.Name, "spool_dir", dir, "err", err)
		return dir, ""
	}
	// The same floor a destination gets, and for the same reason: a disk with
	// nothing left on it breaks everything else running on it, and here that is
	// the machine the user is sitting at.
	want := need + minFreeFloorBytes
	if avail >= want {
		return dir, ""
	}
	log.Warn("this snapshot will be written but NOT checked: there is not room to read it back",
		"archive", job.Name, "spool_dir", dir,
		"needed_bytes", need, "keep_free_bytes", minFreeFloorBytes, "free_bytes", avail)
	return "", fmt.Sprintf(
		"the snapshot was written but not checked: reading it back needs about %s in %s and %s must stay free there, but only %s is left",
		gb(need), dir, gb(minFreeFloorBytes), gb(avail))
}

// spoolGuard fails the spool before it takes the local disk to zero.
//
// The pre-flight above is an estimate taken minutes or hours earlier — the
// archive can be bigger than the last one, and anything else on the machine can
// have eaten the room in between. This is the part that cannot be wrong: it
// measures the actual disk as it writes, and stops.
type spoolGuard struct {
	w     io.Writer
	dir   string
	floor uint64
	since int64
}

// newSpoolGuard starts one chunk overdue, so the disk is measured before the
// FIRST byte lands as well as every 64MB after it. The pre-flight figure is by
// then minutes or hours old — the packing happened in between — and it was an
// estimate to begin with.
func newSpoolGuard(w io.Writer, dir string, floor uint64) *spoolGuard {
	return &spoolGuard{w: w, dir: dir, floor: floor, since: spoolCheckBytes}
}

// Write checks BEFORE handing the bytes on, so the floor is a floor rather than
// a line the last chunk is allowed to cross.
func (g *spoolGuard) Write(p []byte) (int, error) {
	g.since += int64(len(p))
	if g.since >= spoolCheckBytes {
		g.since = 0
		if avail, _, err := localUsage(g.dir); err == nil && avail < g.floor {
			return 0, &spoolFullError{dir: g.dir, free: avail}
		}
	}
	return g.w.Write(p)
}
