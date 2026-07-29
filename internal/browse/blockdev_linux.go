// SPDX-License-Identifier: MIT

//go:build linux

package browse

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"golang.org/x/sys/unix"
)

// Paths this file reads. Variables so tests can point them at a fixture tree
// rather than at the machine running the tests.
var (
	sysBlockDir   = "/sys/block"
	mountInfoPath = "/proc/self/mountinfo"
	swapsPath     = "/proc/swaps"
)

// ListUnusable reports storage that is present but cannot hold backups yet.
//
// This is the only place in the program that looks at a block device, and it
// looks only through sysfs and procfs: no shell-outs, no new dependencies, no
// privileges. It reads sizes and names — never the contents of a disk.
//
// NOTHING HERE IS AUTHORITATIVE. The daemon runs as an ordinary user and
// cannot open /dev/sda to read a filesystem superblock, so "no partitions"
// means the disk *looks* blank. That is enough for a sentence on a screen and
// never enough to act on: prepare-drive re-checks as root, with wipefs, before
// it touches anything.
func ListUnusable() []Unusable {
	return append(blockDevices(), unmountedParents()...)
}

func blockDevices() []Unusable {
	entries, err := os.ReadDir(sysBlockDir)
	if err != nil {
		return nil
	}
	inUse := mountedDevices()
	swap := swapDevices()
	var out []Unusable
	for _, e := range entries {
		d, ok := readDisk(e.Name())
		if !ok {
			continue
		}
		// A disk with anything mounted or swapping on it is either the system
		// disk or a drive Drives() already offers. Either way it is in use,
		// and nothing here should invite the user to reformat it.
		busy := false
		for _, dev := range d.Devs {
			if inUse[dev] {
				busy = true
			}
		}
		for _, p := range d.PartPaths {
			if swap[p] {
				busy = true
			}
		}
		if busy {
			continue
		}

		u := Unusable{
			Device:  d.Device(),
			Name:    d.Name,
			Size:    d.Size,
			Bus:     d.Bus,
			Confirm: d.Confirm(),
		}
		switch {
		case d.ReadOnly:
			u.Reason = ReasonReadOnly
			u.Detail = "Plugged in, but the drive says it is read-only. Backups cannot be written to it. Some drives have a physical write-protect switch."
		case len(d.PartPaths) == 0:
			u.Reason = ReasonBlank
			u.Detail = "Plugged in, but not set up for backups yet: there are no partitions on it."
		default:
			u.Reason = ReasonUnmounted
			u.Detail = "Plugged in and already partitioned, but nothing on it is mounted, so there is nowhere to put backups yet."
		}
		out = append(out, u)
	}
	return out
}

// DiskFacts is everything this package can learn about a whole disk without
// privileges. prepare-drive re-reads it as root before acting, so that the
// browser is never the thing that decided which device gets formatted.
type DiskFacts struct {
	Base string // sda
	Name string // what to show a human
	// Devs holds "major:minor" for the disk and each of its partitions — the
	// exact identity used to ask whether anything on it is mounted, or whether
	// a folder we back up lives on it.
	Devs      []string
	PartPaths []string // /dev/sda1, …
	Size      uint64
	ReadOnly  bool
	Bus       string
}

// Device is the kernel path for the whole disk.
func (d DiskFacts) Device() string { return "/dev/" + d.Base }

// Confirm is the phrase a user types back before this drive is formatted. Both
// the page offering it and the root helper checking it derive the string here,
// so they cannot disagree.
func (d DiskFacts) Confirm() string { return d.Base + " " + HumanSize(d.Size) }

// Disk reads one whole disk by its kernel path, e.g. /dev/sda.
func Disk(device string) (DiskFacts, bool) {
	base := strings.TrimPrefix(device, "/dev/")
	if base == "" || base != filepath.Base(base) {
		return DiskFacts{}, false
	}
	return readDisk(base)
}

func readDisk(base string) (DiskFacts, bool) {
	dir := filepath.Join(sysBlockDir, base)
	resolved, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return DiskFacts{}, false
	}
	// loop, zram, ram and device-mapper devices all live under the virtual
	// bus. None of them is a drive somebody plugged in.
	if strings.Contains(filepath.ToSlash(resolved), "/devices/virtual/") {
		return DiskFacts{}, false
	}
	sectors, err := strconv.ParseUint(readTrim(filepath.Join(dir, "size")), 10, 64)
	if err != nil || sectors == 0 {
		// A card reader with no card in it reports zero and is not worth
		// mentioning to anybody.
		return DiskFacts{}, false
	}
	d := DiskFacts{
		Base: base,
		// The kernel reports size in 512-byte sectors whatever the drive's own
		// block size is.
		Size:     sectors * 512,
		ReadOnly: readTrim(filepath.Join(dir, "ro")) == "1",
		Bus:      busOf(resolved),
	}
	if dev := readTrim(filepath.Join(dir, "dev")); dev != "" {
		d.Devs = append(d.Devs, dev)
	}
	if kids, err := os.ReadDir(dir); err == nil {
		for _, k := range kids {
			if !k.IsDir() {
				continue
			}
			part := filepath.Join(dir, k.Name())
			// A partition directory is the one carrying a "partition" file;
			// matching on the name prefix would also catch things like
			// "sda/subsystem".
			if _, err := os.Stat(filepath.Join(part, "partition")); err != nil {
				continue
			}
			d.PartPaths = append(d.PartPaths, "/dev/"+k.Name())
			if dev := readTrim(filepath.Join(part, "dev")); dev != "" {
				d.Devs = append(d.Devs, dev)
			}
		}
	}
	d.Name = diskName(dir, resolved, base)
	return d, true
}

// InUse reports whether anything is mounted or swapping on this disk, and says
// what. An empty string means the disk is idle.
func (d DiskFacts) InUse() string {
	at := MountPoints()
	for _, dev := range d.Devs {
		if mp, ok := at[dev]; ok {
			return "something is mounted on it, at " + mp
		}
	}
	swap := swapDevices()
	for _, p := range d.PartPaths {
		if swap[p] {
			return "it is in use as swap space (" + p + ")"
		}
	}
	return ""
}

// DeviceOf reports the "major:minor" of the filesystem holding path, so a
// caller can ask whether a directory lives on a given disk.
func DeviceOf(path string) (string, error) {
	var st unix.Stat_t
	if err := unix.Stat(path, &st); err != nil {
		return "", err
	}
	return strconv.FormatUint(uint64(unix.Major(uint64(st.Dev))), 10) + ":" +
		strconv.FormatUint(uint64(unix.Minor(uint64(st.Dev))), 10), nil
}

// diskName prefers the name the drive gives over the bus, because that is what
// is printed on the box the user is holding. The SCSI vendor and model fields
// are a poor second: they are fixed-width, so a name can be chopped across
// them (a Crucial CT500P310SSD8 arrives as vendor "CT500P31", model "0SSD8").
func diskName(dir, resolved, base string) string {
	if p := busProduct(resolved); p != "" {
		return p
	}
	vendor := readTrim(filepath.Join(dir, "device", "vendor"))
	model := readTrim(filepath.Join(dir, "device", "model"))
	switch {
	case vendor != "" && model != "":
		return vendor + " " + model
	case model != "":
		return model
	case vendor != "":
		return vendor
	}
	return base
}

// busProduct walks up the device tree looking for a node that names itself —
// a USB device carries "product" beside "idVendor".
func busProduct(resolved string) string {
	for dir := resolved; ; dir = filepath.Dir(dir) {
		if parent := filepath.Dir(dir); parent == dir || dir == "/" || dir == "/sys" {
			return ""
		}
		if _, err := os.Stat(filepath.Join(dir, "idVendor")); err == nil {
			if p := readTrim(filepath.Join(dir, "product")); p != "" {
				return p
			}
		}
	}
}

func busOf(resolved string) string {
	p := filepath.ToSlash(resolved)
	switch {
	case strings.Contains(p, "/usb"):
		return "usb"
	case strings.Contains(p, "/nvme"):
		return "nvme"
	case strings.Contains(p, "/mmc"):
		return "mmc"
	}
	return ""
}

// mountedDevices maps "major:minor" to true for everything currently mounted.
func mountedDevices() map[string]bool {
	out := map[string]bool{}
	for _, f := range mountInfoFields() {
		if len(f) > 2 {
			out[f[2]] = true
		}
	}
	return out
}

// MountPoints maps "major:minor" to where that filesystem is mounted.
func MountPoints() map[string]string {
	out := map[string]string{}
	for _, f := range mountInfoFields() {
		if len(f) > 4 {
			out[f[2]] = unescapeMount(f[4])
		}
	}
	return out
}

func mountInfoFields() [][]string {
	b, err := os.ReadFile(mountInfoPath)
	if err != nil {
		return nil
	}
	var out [][]string
	for _, line := range strings.Split(string(b), "\n") {
		if f := strings.Fields(line); len(f) > 4 {
			out = append(out, f)
		}
	}
	return out
}

// swapDevices lists devices in use as swap. They never appear in mountinfo, so
// without this a swap disk would look blank and be offered for formatting.
func swapDevices() map[string]bool {
	out := map[string]bool{}
	b, err := os.ReadFile(swapsPath)
	if err != nil {
		return out
	}
	for i, line := range strings.Split(string(b), "\n") {
		if i == 0 { // header
			continue
		}
		if f := strings.Fields(line); len(f) > 0 {
			out[f[0]] = true
		}
	}
	return out
}

// unescapeMount undoes the octal escaping the kernel applies to mount points
// containing spaces, tabs, newlines or backslashes.
func unescapeMount(s string) string {
	if !strings.Contains(s, `\`) {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+3 < len(s) {
			if n, err := strconv.ParseUint(s[i+1:i+4], 8, 8); err == nil {
				b.WriteByte(byte(n))
				i += 3
				continue
			}
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

func readTrim(path string) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}
