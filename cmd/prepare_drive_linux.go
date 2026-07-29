// SPDX-License-Identifier: MIT

//go:build linux

package cmd

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/phil9922/backup-maker/internal/browse"
	"github.com/phil9922/backup-maker/internal/config"
	"github.com/phil9922/backup-maker/internal/drivesetup"
)

// prepareDrive formats a blank disk and mounts it as a backup destination.
//
// THE ONLY CODE IN THIS PROGRAM THAT DESTROYS A FILESYSTEM, and the only code
// that runs as root, so every guard below is load-bearing and none of them is
// decided in the browser:
//
//  1. The device must be a whole disk, named exactly, with no symlink and no
//     path trickery. The dashboard sends a string; a string is not a disk.
//  2. Nothing may be mounted or swapping on it. That is what keeps the system
//     disk, and any drive already holding backups, out of reach.
//  3. wipefs must find no signature of any kind. A drive with anything at all
//     on it is refused outright — this alone removes almost every way to lose
//     data here, and --force is deliberately not reachable from the dashboard.
//  4. The confirmation phrase must match the one derived from the device
//     itself, so approving the wrong row cannot format the wrong disk.
//  5. No folder this computer backs up may live on the disk, and the mount
//     point may not be inside one. NEVER DELETE FILES FROM A SOURCE FOLDER is
//     the rule this program is built around; formatting the disk one lives on
//     would be the most complete way to break it.
//
// The unprivileged detection in internal/browse is advisory only. Everything
// that matters is re-read here, as root, at the moment of acting.
func prepareDrive(o prepareOpts, out io.Writer) error {
	if os.Geteuid() != 0 {
		return fmt.Errorf("preparing a drive needs root. Run:\n  sudo %s prepare-drive --device %s --mount %s --confirm %q",
			exeName(), o.device, o.mount, o.confirm)
	}
	if err := wholeDiskPath(o.device); err != nil {
		return err
	}
	facts, ok := browse.Disk(o.device)
	if !ok {
		return fmt.Errorf("%s is not a whole disk on this computer", o.device)
	}
	if facts.ReadOnly {
		return fmt.Errorf("%s says it is read-only, so nothing can be written to it. Some drives have a physical write-protect switch", o.device)
	}
	if why := facts.InUse(); why != "" {
		return fmt.Errorf("refusing to touch %s: %s. Preparing a drive erases it, so this command only ever works on a drive nothing is using", o.device, why)
	}
	if err := notInFstab(o.device, o.mount); err != nil {
		return err
	}
	if !o.force {
		if err := nothingOnIt(o.device); err != nil {
			return err
		}
	}
	if strings.TrimSpace(o.confirm) != facts.Confirm() {
		return fmt.Errorf("nothing was changed: the phrase typed did not match %q", facts.Confirm())
	}
	if err := notASourceDisk(facts, o.mount); err != nil {
		return err
	}
	if err := usableMountPoint(o.mount); err != nil {
		return err
	}

	part := partitionOf(o.device)
	steps := []step{
		{"create a new partition table and one partition filling the drive",
			[]string{"sgdisk", "--clear", "--new=1:0:0", "--typecode=1:8300", o.device}},
		{"make an ext4 filesystem on " + part,
			// -m 1 leaves 1% for root rather than the default 5%. On a 500GB
			// backup drive the default holds back 25GB for a purpose a backup
			// destination does not have.
			[]string{"mkfs.ext4", "-q", "-m", "1", "-L", o.label, part}},
	}
	if o.dryRun {
		fmt.Fprintf(out, "Would prepare %s (%s, %s) and mount it at %s:\n\n",
			o.device, facts.Name, browse.HumanSize(facts.Size), o.mount)
		for _, s := range steps {
			fmt.Fprintf(out, "  # %s\n  %s\n", s.why, strings.Join(s.argv, " "))
		}
		fmt.Fprintf(out, "  # remember it by UUID so it lands here after every reboot\n")
		fmt.Fprintf(out, "  echo 'UUID=<the new uuid>  %s  ext4  defaults,noatime,nofail  0  2' >> /etc/fstab\n", o.mount)
		fmt.Fprintf(out, "  mount %s\n\nNothing was changed.\n", o.mount)
		return nil
	}

	if err := os.MkdirAll(o.mount, 0o755); err != nil {
		return fmt.Errorf("cannot create the mount point %s: %w", o.mount, err)
	}
	// Make the bare directory immutable BEFORE anything is mounted over it, so
	// that if the drive ever fails to appear, writes fail loudly instead of
	// quietly filling this computer's own disk with backups nobody can find.
	// Best-effort: not every filesystem supports the flag.
	protectMountPoint(o.mount, out)

	for _, s := range steps {
		fmt.Fprintf(out, "%s…\n", capitalise(s.why))
		if err := runTool(s.argv); err != nil {
			return err
		}
	}
	if err := waitForNode(part); err != nil {
		return err
	}
	uuid, err := filesystemUUID(part)
	if err != nil {
		return err
	}
	if err := addFstabEntry(uuid, o.mount); err != nil {
		return err
	}
	fmt.Fprintf(out, "Mounting it at %s…\n", o.mount)
	if err := runTool([]string{"mount", o.mount}); err != nil {
		return err
	}
	if err := giveToInvoker(o.mount); err != nil {
		return err
	}
	fmt.Fprintf(out, "\nDone. %s is ready to hold backups.\n", o.mount)
	return nil
}

type step struct {
	why  string
	argv []string
}

// wholeDiskPath accepts only the kernel's own name for a whole disk. A
// partition, a symlink, a /dev/disk/by-* alias or anything with a directory
// separator in it is refused rather than resolved: this string arrives from a
// web request, and the safe response to an unexpected shape is to stop.
func wholeDiskPath(device string) error {
	if device != filepath.Clean(device) || !wholeDisk.MatchString(device) {
		return fmt.Errorf("%q is not a whole disk. Expected something like /dev/sda, /dev/nvme0n1 or /dev/mmcblk0 — not a partition and not a /dev/disk/by-id alias", device)
	}
	return blockDeviceAt(device)
}

// blockDeviceAt checks that a path really is a block device and not a link to
// one. Lstat, not Stat: a symlink at a name that passes the pattern above
// could still point at the system disk, and following it would be the program
// choosing which disk to erase on the strength of something it did not write.
func blockDeviceAt(path string) error {
	fi, err := os.Lstat(path)
	if err != nil {
		return fmt.Errorf("no such device: %s", path)
	}
	if fi.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("%s is a symlink; name the real device instead", path)
	}
	if fi.Mode()&os.ModeDevice == 0 || fi.Mode()&os.ModeCharDevice != 0 {
		return fmt.Errorf("%s is not a block device", path)
	}
	return nil
}

var wholeDisk = regexp.MustCompile(`^/dev/(sd[a-z]{1,2}|hd[a-z]|vd[a-z]|nvme\d+n\d+|mmcblk\d+)$`)

// fstabPath is a variable so tests can use a file of their own rather than the
// running machine's.
var fstabPath = "/etc/fstab"

// nothingOnIt refuses any drive carrying a filesystem, partition table or RAID
// signature. wipefs -n reports what it finds without changing anything.
func nothingOnIt(device string) error {
	outBytes, err := exec.Command(mustTool("wipefs"), "-n", device).CombinedOutput()
	if err != nil {
		return fmt.Errorf("could not check whether %s is empty: %w", device, err)
	}
	if found := strings.TrimSpace(string(outBytes)); found != "" {
		return fmt.Errorf("refusing to erase %s: there is already something on it.\n%s\nIf this really is the drive you meant, erase it yourself first — that way the decision is yours and it is recoverable up to the moment you make it", device, found)
	}
	return nil
}

// notASourceDisk keeps the program's central promise. A folder being backed up
// must never be destroyed by the thing protecting it.
func notASourceDisk(facts browse.DiskFacts, mount string) error {
	cfg, err := config.Load()
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil // nothing is being backed up on this machine yet
		}
		return fmt.Errorf("cannot read the configuration, so cannot check this drive holds nothing we back up: %w", err)
	}
	for _, f := range cfg.Folders {
		dev, err := browse.DeviceOf(f.Path)
		if err == nil && slices.Contains(facts.Devs, dev) {
			return fmt.Errorf("refusing to erase %s: %q (%s), a folder this computer backs up, is on that drive", facts.Device(), f.Label, f.Path)
		}
		if insideOrEqual(mount, f.Path) || insideOrEqual(f.Path, mount) {
			return fmt.Errorf("refusing: the mount point %s overlaps %q (%s), a folder this computer backs up. A backup destination must be somewhere separate from the things being backed up", mount, f.Label, f.Path)
		}
	}
	return nil
}

func usableMountPoint(mount string) error {
	if !filepath.IsAbs(mount) || mount != filepath.Clean(mount) {
		return fmt.Errorf("the mount point must be a plain absolute path, e.g. /mnt/backups (got %q)", mount)
	}
	for _, reserved := range []string{"/", "/bin", "/boot", "/dev", "/etc", "/home", "/lib", "/proc", "/root", "/run", "/sbin", "/sys", "/usr", "/var"} {
		if mount == reserved {
			return fmt.Errorf("%s belongs to the system; mount the drive somewhere of its own, such as /mnt/backups", mount)
		}
	}
	entries, err := os.ReadDir(mount)
	if os.IsNotExist(err) {
		return nil // it gets created
	}
	if err != nil {
		return fmt.Errorf("cannot look at %s: %w", mount, err)
	}
	if len(entries) > 0 {
		return fmt.Errorf("%s is not empty. Mounting a drive over it would hide what is in there, so pick an empty directory or clear that one out first", mount)
	}
	return nil
}

// notInFstab refuses to add a second entry for a device or a mount point that
// is already spoken for, rather than leaving a file with two answers in it.
func notInFstab(device, mount string) error {
	b, err := os.ReadFile(fstabPath)
	if err != nil {
		return nil
	}
	for _, line := range strings.Split(string(b), "\n") {
		f := strings.Fields(line)
		if len(f) < 2 || strings.HasPrefix(f[0], "#") {
			continue
		}
		if f[0] == device {
			return fmt.Errorf("/etc/fstab already has an entry for %s; this computer is already set up to mount it somewhere", device)
		}
		if f[1] == mount {
			return fmt.Errorf("/etc/fstab already mounts something at %s. Remove that line first if you mean to replace it", mount)
		}
	}
	return nil
}

func addFstabEntry(uuid, mount string) error {
	// nofail is the difference between a missing drive being an inconvenience
	// and a machine that will not boot without a screen and keyboard attached.
	line := fmt.Sprintf("UUID=%s  %s  ext4  defaults,noatime,nofail  0  2\n", uuid, mount)
	f, err := os.OpenFile(fstabPath, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("cannot record the drive in /etc/fstab: %w", err)
	}
	defer f.Close()
	if _, err := f.WriteString(line); err != nil {
		return fmt.Errorf("cannot record the drive in /etc/fstab: %w", err)
	}
	return nil
}

func filesystemUUID(part string) (string, error) {
	b, err := exec.Command(mustTool("blkid"), "-s", "UUID", "-o", "value", part).Output()
	if uuid := strings.TrimSpace(string(b)); err == nil && uuid != "" {
		return uuid, nil
	}
	return "", fmt.Errorf("the new filesystem on %s has no UUID to remember it by", part)
}

// giveToInvoker hands the mounted filesystem to the user who asked for it. A
// fresh ext4 root belongs to root, so without this the daemon — which is not
// root — could not write a single file to the drive it just prepared.
func giveToInvoker(mount string) error {
	uid, err1 := strconv.Atoi(os.Getenv("SUDO_UID"))
	gid, err2 := strconv.Atoi(os.Getenv("SUDO_GID"))
	if err1 != nil || err2 != nil {
		return nil // run as root directly; leave ownership alone
	}
	if err := os.Chown(mount, uid, gid); err != nil {
		return fmt.Errorf("the drive is mounted but still owned by root, so backups cannot be written to it: %w", err)
	}
	return nil
}

func protectMountPoint(mount string, out io.Writer) {
	chattr, err := findTool("chattr")
	if err != nil {
		return
	}
	if err := exec.Command(chattr, "+i", mount).Run(); err != nil {
		fmt.Fprintf(out, "note: could not write-protect the bare %s. If the drive ever fails to mount, backups would go to this computer's own disk instead.\n", mount)
	}
}

// waitForNode gives udev a moment to create the partition's device node, which
// does not exist the instant the partition table is written.
func waitForNode(part string) error {
	if settle, err := findTool("udevadm"); err == nil {
		_ = exec.Command(settle, "settle").Run()
	}
	for i := 0; i < 50; i++ {
		if _, err := os.Stat(part); err == nil {
			return nil
		}
		time.Sleep(200 * time.Millisecond)
	}
	return fmt.Errorf("the partition %s never appeared after the partition table was written", part)
}

// partitionOf names the first partition of a disk. Devices whose names end in
// a digit take a "p" first: /dev/sda1 but /dev/nvme0n1p1.
func partitionOf(device string) string {
	if last := device[len(device)-1]; last >= '0' && last <= '9' {
		return device + "p1"
	}
	return device + "1"
}

func insideOrEqual(inner, outer string) bool {
	rel, err := filepath.Rel(filepath.Clean(outer), filepath.Clean(inner))
	if err != nil {
		return false
	}
	return rel == "." || (!strings.HasPrefix(rel, ".."+string(filepath.Separator)) && rel != "..")
}

// findTool looks in the directories root's PATH would have, because the daemon
// that invokes this through sudo may not have /sbin on its own.
func findTool(name string) (string, error) {
	for _, dir := range []string{"/sbin", "/usr/sbin", "/bin", "/usr/bin", "/usr/local/sbin", "/usr/local/bin"} {
		p := filepath.Join(dir, name)
		if fi, err := os.Stat(p); err == nil && !fi.IsDir() {
			return p, nil
		}
	}
	return exec.LookPath(name)
}

func mustTool(name string) string {
	p, err := findTool(name)
	if err != nil {
		return name
	}
	return p
}

func runTool(argv []string) error {
	path, err := findTool(argv[0])
	if err != nil {
		return fmt.Errorf("%s is not installed on this computer, and it is needed to prepare a drive", argv[0])
	}
	cmd := exec.Command(path, argv[1:]...)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("%s failed: %w\n%s", argv[0], err, strings.TrimSpace(string(out)))
	}
	return nil
}

func capitalise(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

func exeName() string {
	if p, err := os.Executable(); err == nil {
		return p
	}
	return "backup-maker"
}

// installSudoers lets the dashboard run prepare-drive as root, after showing
// the user the exact rule and having them agree to it.
//
// THE RULE NAMES ONE EXACT COMMAND WITH NO ARGUMENTS. It used to end in a
// wildcard, and a sudoers wildcard matches any further arguments, whitespace
// included — so it also granted a passwordless `prepare-drive --force …`, which
// skips the refusal of a drive that already has something on it. That refusal
// is the guard this whole design leans on, and the rule was quietly handing out
// permission to skip it. The request now travels on stdin, where it cannot
// widen what sudo agreed to.
//
// Nothing in the drive flow requires this rule: without it the dashboard shows
// the commands to paste instead.
func installSudoers(out io.Writer) error {
	if os.Geteuid() != 0 {
		return fmt.Errorf("installing the rule needs root. Run:\n  sudo %s prepare-drive --install-sudoers", exeName())
	}
	user := os.Getenv("SUDO_USER")
	if user == "" {
		return fmt.Errorf("run this with sudo from your own account, so the rule can name the user the dashboard runs as")
	}
	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("cannot work out where this program lives: %w", err)
	}
	if strings.ContainsAny(exe, ", \t\\") {
		return fmt.Errorf("the path to this program (%s) contains a character that cannot appear in a sudoers rule; move the binary somewhere plainer, such as /usr/local/bin", exe)
	}
	if err := onlyRootCanReplace(exe); err != nil {
		return err
	}
	rule := fmt.Sprintf("%s ALL=(root) NOPASSWD: %s %s\n", user, exe, strings.Join(drivesetup.PrivilegedArgs(), " "))

	fmt.Fprintf(out, "This will add the following to /etc/sudoers.d/backup-maker:\n\n  %s\n", rule)
	fmt.Fprintf(out, "It lets %s prepare a drive from the dashboard without typing a password.\n", user)
	fmt.Fprintf(out, "prepare-drive still refuses any drive that has anything on it, any drive\n")
	fmt.Fprintf(out, "something is mounted from, and any drive holding a folder you back up.\n\n")
	fmt.Fprintf(out, "Remove it later with:  sudo rm /etc/sudoers.d/backup-maker\n\n")
	fmt.Fprint(out, "Type yes to add it: ")

	var answer string
	if _, err := fmt.Scanln(&answer); err != nil || strings.TrimSpace(answer) != "yes" {
		fmt.Fprintln(out, "Nothing was changed.")
		return nil
	}

	// Validate before installing: a malformed file in sudoers.d can lock the
	// user out of sudo entirely.
	tmp, err := os.CreateTemp("", "backup-maker-sudoers")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.WriteString(rule); err != nil {
		return err
	}
	tmp.Close()
	if visudo, err := findTool("visudo"); err == nil {
		if outBytes, err := exec.Command(visudo, "-c", "-f", tmp.Name()).CombinedOutput(); err != nil {
			return fmt.Errorf("the rule was rejected as invalid, so nothing was installed:\n%s", strings.TrimSpace(string(outBytes)))
		}
	}
	if err := os.WriteFile(sudoersPath, []byte(rule), 0o440); err != nil {
		return fmt.Errorf("cannot write %s: %w", sudoersPath, err)
	}
	fmt.Fprintf(out, "\nDone. The dashboard can now prepare a drive on this computer.\n")
	return nil
}

const sudoersPath = "/etc/sudoers.d/backup-maker"

// onlyRootCanReplace refuses to grant passwordless root to a binary that
// somebody other than root can swap out.
//
// THIS IS THE WHOLE POINT OF THE RULE'S SAFETY. Every guard that decides what
// may be erased lives inside this binary — so a NOPASSWD rule naming a file the
// user can overwrite does not grant "the right to prepare a blank drive", it
// grants root, full stop: replace the file, run the command, be root. On the
// machine this was written on the binary sat in ~/.local/bin, owned by the user
// and group-writable, and this program is normally pointed at a folder full of
// third-party package installs — a hostile postinstall script is not a
// hypothetical way for something to be running as that user.
//
// Every directory above it counts too: write permission on a parent is
// permission to replace what is inside it.
func onlyRootCanReplace(path string) error {
	for p := filepath.Clean(path); ; p = filepath.Dir(p) {
		fi, err := os.Lstat(p)
		if err != nil {
			return fmt.Errorf("cannot check who can change %s: %w", p, err)
		}
		if fi.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("%s is a symlink. A sudoers rule must name the real file, or whoever can move the link decides what runs as root", p)
		}
		st, ok := fi.Sys().(*syscall.Stat_t)
		if !ok {
			return fmt.Errorf("cannot check who owns %s on this system", p)
		}
		if st.Uid != 0 || fi.Mode().Perm()&0o022 != 0 {
			return fmt.Errorf(`refusing to grant passwordless root to %s, because %s can be changed by someone who is not root (owner uid %d, mode %v).

Anyone who can replace that file could then run anything as root, and every check that decides which drive may be erased lives inside it.

Put the program somewhere only root can write, and run this again from there:
  sudo install -o root -g root -m 755 %s /usr/local/bin/backup-maker
  sudo /usr/local/bin/backup-maker prepare-drive --install-sudoers

Or skip the rule entirely: the dashboard will show you the exact command to paste, and you type your own password`, path, p, st.Uid, fi.Mode().Perm(), path)
		}
		if p == "/" {
			return nil
		}
	}
}
