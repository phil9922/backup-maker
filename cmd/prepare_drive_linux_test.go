// SPDX-License-Identifier: MIT

//go:build linux

package cmd

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/phil9922/backup-maker/internal/browse"
	"github.com/phil9922/backup-maker/internal/config"
)

func isolate(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	t.Setenv("HOME", dir)
	cfg := config.New()
	if err := cfg.Save(); err != nil {
		t.Fatal(err)
	}
}

// backingUp records path as a folder this computer protects.
func backingUp(t *testing.T, label, path string) {
	t.Helper()
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	cfg.Folders = append(cfg.Folders, config.Folder{ID: "f1", Path: path, Label: label})
	if err := cfg.Save(); err != nil {
		t.Fatal(err)
	}
}

// The rule this whole program is built around: a folder being backed up is
// never destroyed by the thing protecting it. Formatting the disk one lives on
// is the most complete way to break that, so prove it cannot happen.
func TestPreparingADriveNeverFormatsADiskHoldingAFolderWeBackUp(t *testing.T) {
	isolate(t)
	source := t.TempDir()
	if err := os.WriteFile(filepath.Join(source, "original.txt"), []byte("mine"), 0o644); err != nil {
		t.Fatal(err)
	}
	backingUp(t, "development", source)

	// A disk whose device numbers include the one the source folder is on —
	// which is what "this folder is on that drive" means to the kernel.
	dev, err := browse.DeviceOf(source)
	if err != nil {
		t.Fatal(err)
	}
	facts := browse.DiskFacts{Base: "sda", Devs: []string{dev}}

	err = notASourceDisk(facts, "/mnt/backups")
	if err == nil {
		t.Fatal("a drive holding a folder we back up was accepted for formatting")
	}
	if !strings.Contains(err.Error(), "development") {
		t.Errorf("the refusal does not name the folder it protected: %v", err)
	}
	if _, err := os.Stat(filepath.Join(source, "original.txt")); err != nil {
		t.Fatalf("the source folder was touched: %v", err)
	}
}

// The mount point is created and then written to, so it must not land inside
// something we back up either — in both directions.
func TestPreparingADriveRefusesAMountPointOverlappingASourceFolder(t *testing.T) {
	for _, tc := range []struct{ name, mount string }{
		{"the folder itself", ""},
		{"inside the folder", "/nested/deeper"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			isolate(t)
			source := t.TempDir()
			backingUp(t, "taxes", source)

			if err := notASourceDisk(browse.DiskFacts{Base: "sda"}, source+tc.mount); err == nil {
				t.Fatal("a mount point overlapping a folder we back up was accepted")
			}
		})
	}
	t.Run("a source folder inside the mount point", func(t *testing.T) {
		isolate(t)
		parent := t.TempDir()
		source := filepath.Join(parent, "code")
		if err := os.MkdirAll(source, 0o755); err != nil {
			t.Fatal(err)
		}
		backingUp(t, "code", source)

		if err := notASourceDisk(browse.DiskFacts{Base: "sda"}, parent); err == nil {
			t.Fatal("mounting a drive over a folder we back up was accepted")
		}
	})
}

// The device name arrives from a web request. Anything that is not exactly a
// whole disk is refused rather than cleaned up and used.
func TestPreparingADriveRefusesAHandEditedDevicePath(t *testing.T) {
	for _, bad := range []struct{ name, device string }{
		{"a partition, not the disk", "/dev/sda1"},
		{"parent traversal", "/dev/../dev/sda"},
		{"a by-id alias", "/dev/disk/by-id/usb-Ugreen"},
		{"trailing slash", "/dev/sda/"},
		{"empty", ""},
		{"not a device at all", "/etc/passwd"},
		{"relative", "dev/sda"},
		{"a nested path", "/dev/mapper/vg-root"},
	} {
		t.Run(bad.name, func(t *testing.T) {
			if err := wholeDiskPath(bad.device); err == nil {
				t.Fatalf("%q was accepted as a disk to erase", bad.device)
			}
		})
	}
}

// A symlink could point anywhere, including at the system disk, so the real
// device has to be named.
func TestPreparingADriveRefusesASymlinkedDevice(t *testing.T) {
	dir := t.TempDir()
	link := filepath.Join(dir, "sda")
	if err := os.Symlink("/dev/null", link); err != nil {
		t.Fatal(err)
	}
	err := blockDeviceAt(link)
	if err == nil {
		t.Fatal("a symlink was accepted as a disk to erase")
	}
	if !strings.Contains(err.Error(), "symlink") {
		t.Errorf("the refusal does not say what was wrong: %v", err)
	}

	// And an ordinary file, in case the name happens to look like a device.
	plain := filepath.Join(dir, "sdb")
	if err := os.WriteFile(plain, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if blockDeviceAt(plain) == nil {
		t.Fatal("a plain file was accepted as a disk to erase")
	}

	// A real block device passes, so the check is not simply refusing
	// everything: /dev/loop0 exists on any Linux that can run these tests.
	if _, err := os.Stat("/dev/loop0"); err == nil {
		if err := blockDeviceAt("/dev/loop0"); err != nil {
			t.Errorf("a real block device was refused: %v", err)
		}
	}
}

func TestPreparingADriveRefusesAnUnusableMountPoint(t *testing.T) {
	occupied := t.TempDir()
	if err := os.WriteFile(filepath.Join(occupied, "someone-elses-file"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ name, mount string }{
		{"the root of the filesystem", "/"},
		{"a system directory", "/etc"},
		{"relative", "mnt/backups"},
		{"unclean", "/mnt/../mnt/backups/"},
		{"a directory with things in it", occupied},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := usableMountPoint(tc.mount); err == nil {
				t.Fatalf("%q was accepted as a mount point", tc.mount)
			}
		})
	}
	t.Run("an empty directory is fine", func(t *testing.T) {
		if err := usableMountPoint(t.TempDir()); err != nil {
			t.Fatalf("an empty directory was refused: %v", err)
		}
	})
	t.Run("a directory that does not exist yet is fine", func(t *testing.T) {
		if err := usableMountPoint(filepath.Join(t.TempDir(), "backups")); err != nil {
			t.Fatalf("a new directory was refused: %v", err)
		}
	})
}

// Two entries for one mount point leaves a file with two answers in it.
func TestPreparingADriveRefusesWhatFstabAlreadyClaims(t *testing.T) {
	dir := t.TempDir()
	fstabPath = filepath.Join(dir, "fstab")
	t.Cleanup(func() { fstabPath = "/etc/fstab" })
	if err := os.WriteFile(fstabPath, []byte(
		"# a comment\nUUID=abc  /mnt/backups  ext4  defaults  0  2\n/dev/sdb  /mnt/other  ext4  defaults  0  2\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := notInFstab("/dev/sda", "/mnt/backups"); err == nil {
		t.Error("a mount point already in fstab was accepted")
	}
	if err := notInFstab("/dev/sdb", "/mnt/elsewhere"); err == nil {
		t.Error("a device already in fstab was accepted")
	}
	if err := notInFstab("/dev/sdc", "/mnt/fresh"); err != nil {
		t.Errorf("an unclaimed device and mount point were refused: %v", err)
	}
}

// Without root nothing can happen, and saying so with the command to run is
// more use than a permissions error.
func TestPreparingADriveWithoutRootSaysHowToGetIt(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("these tests are not meant to be run as root")
	}
	err := prepareDrive(prepareOpts{device: "/dev/sda", mount: "/mnt/backups"}, io.Discard)
	if err == nil {
		t.Fatal("prepare-drive ran without root")
	}
	if !strings.Contains(err.Error(), "sudo") {
		t.Errorf("the refusal does not say how to proceed: %v", err)
	}
}

func TestPartitionNamesFollowTheKernelsConvention(t *testing.T) {
	for _, tc := range []struct{ device, want string }{
		{"/dev/sda", "/dev/sda1"},
		{"/dev/nvme0n1", "/dev/nvme0n1p1"},
		{"/dev/mmcblk0", "/dev/mmcblk0p1"},
	} {
		if got := partitionOf(tc.device); got != tc.want {
			t.Errorf("partitionOf(%q) = %q, want %q", tc.device, got, tc.want)
		}
	}
}

func TestPathContainmentIsNotFooledByANameThatMerelyStartsTheSame(t *testing.T) {
	if insideOrEqual("/mnt/backups-old", "/mnt/backups") {
		t.Error("/mnt/backups-old was treated as being inside /mnt/backups")
	}
	if !insideOrEqual("/mnt/backups/machine", "/mnt/backups") {
		t.Error("a real subdirectory was not recognised")
	}
	if !insideOrEqual("/mnt/backups", "/mnt/backups") {
		t.Error("a path was not recognised as equal to itself")
	}
}

// THE GUARANTEE: passwordless root is never granted to a file somebody other
// than root can replace.
//
// Every check that decides which drive may be erased lives inside this binary.
// A NOPASSWD sudoers rule naming a file the user can overwrite therefore does
// not grant "the right to prepare a blank drive" — it grants root outright:
// replace the file, run the command, be root. The binary sat in ~/.local/bin,
// user-owned and group-writable, on a machine whose whole purpose is a folder
// full of third-party package installs.
func TestPasswordlessRootIsRefusedForABinaryTheUserCanReplace(t *testing.T) {
	dir := t.TempDir() // owned by the test user, not root
	exe := filepath.Join(dir, "backup-maker")
	if err := os.WriteFile(exe, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	err := onlyRootCanReplace(exe)
	if err == nil {
		t.Fatal("granting passwordless root to a user-owned binary was allowed")
	}
	// The refusal has to say what to do about it, or it is a dead end.
	for _, want := range []string{"root", "/usr/local/bin"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal does not mention %q:\n%s", want, err)
		}
	}
}

// A writable DIRECTORY anywhere above the binary is the same hole: permission
// to write a directory is permission to replace what is in it.
func TestAWritableDirectoryAboveTheBinaryIsRefusedToo(t *testing.T) {
	// /tmp is world-writable, so any path under it fails on a parent even when
	// the file itself looks fine.
	dir := t.TempDir()
	exe := filepath.Join(dir, "backup-maker")
	if err := os.WriteFile(exe, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := onlyRootCanReplace(exe); err == nil {
		t.Error("a binary under a writable parent directory was accepted")
	}
}

// A symlink must be refused rather than followed: whoever can move the link
// decides what runs as root.
func TestASymlinkedBinaryIsNeverNamedInTheRule(t *testing.T) {
	dir := t.TempDir()
	real := filepath.Join(dir, "real")
	if err := os.WriteFile(real, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "link")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	err := onlyRootCanReplace(link)
	if err == nil {
		t.Fatal("a symlink was accepted as the target of a passwordless root rule")
	}
	if !strings.Contains(err.Error(), "symlink") {
		t.Errorf("the refusal does not explain that it is a symlink:\n%s", err)
	}
}

// Something genuinely root-owned and not group- or world-writable is accepted,
// or the check would refuse every correct installation too.
//
// THE PREMISE IS VERIFIED, NOT ASSUMED. This used to name /usr/bin/env with a
// comment claiming it "is root-owned and 755 on every system this runs on". That
// is not true of a GitHub runner, where /usr can be owned by the build user — so
// onlyRootCanReplace refused it, entirely correctly, and the test failed for
// being wrong about the machine rather than about the code. A test cannot assert
// what a filesystem it does not own looks like.
//
// So: find a candidate whose whole ancestor chain really is root-owned, checked
// here with an independent walk rather than by asking the function under test,
// and skip loudly if this machine has none. The refusal cases above own their
// fixtures and run everywhere; this one cannot, because a test process that is
// not root cannot create a root-owned file.
func TestARootOwnedBinaryIsAccepted(t *testing.T) {
	var why string
	for _, cand := range []string{"/usr/bin/env", "/bin/sh", "/usr/bin/true", "/sbin/init"} {
		if _, err := os.Lstat(cand); err != nil {
			continue
		}
		if bad := firstNonRootAncestor(cand); bad != "" {
			if why == "" {
				why = cand + ": " + bad + " is not root-owned or is writable by others"
			}
			continue
		}
		if err := onlyRootCanReplace(cand); err != nil {
			t.Errorf("a properly installed root-owned binary was refused: %v", err)
		}
		return
	}
	t.Skipf("no root-owned executable on this machine to check the accept path "+
		"against (%s)", why)
}

// The premise-checker above decides whether the accept path is exercised or
// skipped, so it gets its own test: if it wrongly reported every chain as safe,
// the test would go back to failing on machines whose /usr is not root-owned,
// which is the flake it exists to prevent.
func TestThePremiseCheckerSpotsANonRootAncestor(t *testing.T) {
	dir := t.TempDir() // owned by whoever runs the test, which is not root
	f := filepath.Join(dir, "prog")
	if err := os.WriteFile(f, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if got := firstNonRootAncestor(f); got == "" {
		t.Error("a file under a user-owned temp directory was reported as having " +
			"an entirely root-owned ancestor chain")
	}
	if got := firstNonRootAncestor("/"); got != "" {
		t.Errorf("/ was reported unsafe (%s); on a sane system it is root-owned "+
			"and not world-writable, and if it is not, the skip message will say so", got)
	}
}

// firstNonRootAncestor names the first path from p up to / that is a symlink, is
// not owned by root, or is group- or world-writable — "" if every one of them is
// safe. Deliberately a second implementation of onlyRootCanReplace's rule: a test
// that decided the premise by calling the function under test would agree with
// itself no matter what either of them did.
func firstNonRootAncestor(p string) string {
	for q := filepath.Clean(p); ; q = filepath.Dir(q) {
		fi, err := os.Lstat(q)
		if err != nil {
			return q
		}
		if fi.Mode()&os.ModeSymlink != 0 {
			return q
		}
		st, ok := fi.Sys().(*syscall.Stat_t)
		if !ok {
			return q
		}
		if st.Uid != 0 || fi.Mode().Perm()&0o022 != 0 {
			return q
		}
		if q == "/" {
			return ""
		}
	}
}
