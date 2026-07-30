// SPDX-License-Identifier: MIT

//go:build linux

package browse

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// sysfs builds a fixture standing in for /sys and /proc. Every test here reads
// through the real code path — real symlinks, real file reads — because the
// parsing this package does is precisely the thing that would go wrong, and a
// mock of sysfs would only prove that the mock matches the code.
type sysfs struct {
	t    *testing.T
	root string
}

func newSysfs(t *testing.T) *sysfs {
	t.Helper()
	s := &sysfs{t: t, root: t.TempDir()}
	s.write("proc/mountinfo", "")
	s.write("proc/swaps", "Filename\t\t\t\tType\t\tSize\t\tUsed\t\tPriority\n")
	sysBlockDir = filepath.Join(s.root, "sys", "block")
	mountInfoPath = filepath.Join(s.root, "proc", "mountinfo")
	swapsPath = filepath.Join(s.root, "proc", "swaps")
	t.Cleanup(func() {
		sysBlockDir, mountInfoPath, swapsPath = "/sys/block", "/proc/self/mountinfo", "/proc/swaps"
	})
	s.mkdir("sys/block")
	return s
}

func (s *sysfs) write(rel, content string) {
	s.t.Helper()
	p := filepath.Join(s.root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		s.t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		s.t.Fatal(err)
	}
}

func (s *sysfs) mkdir(rel string) {
	s.t.Helper()
	if err := os.MkdirAll(filepath.Join(s.root, filepath.FromSlash(rel)), 0o755); err != nil {
		s.t.Fatal(err)
	}
}

// disk adds a whole disk at devPath (a location under sys/devices) and links
// /sys/block/<name> to it, the way the kernel does.
func (s *sysfs) disk(name, devPath, majMin string, sectors uint64, ro bool) string {
	s.t.Helper()
	base := devPath + "/block/" + name
	s.write(base+"/size", itoaU(sectors)+"\n")
	s.write(base+"/dev", majMin+"\n")
	s.write(base+"/ro", map[bool]string{true: "1", false: "0"}[ro]+"\n")
	s.write(base+"/removable", "0\n")
	target := filepath.Join(s.root, filepath.FromSlash(base))
	link := filepath.Join(s.root, "sys", "block", name)
	if err := os.Symlink(target, link); err != nil {
		s.t.Fatal(err)
	}
	return base
}

func (s *sysfs) partition(diskBase, name, majMin string) {
	s.t.Helper()
	s.write(diskBase+"/"+name+"/partition", "1\n")
	s.write(diskBase+"/"+name+"/dev", majMin+"\n")
}

func (s *sysfs) usbEnclosure(devPath, product string) {
	s.t.Helper()
	s.write(devPath+"/idVendor", "0bda\n")
	s.write(devPath+"/product", product+"\n")
}

func (s *sysfs) mounted(majMin, at string) {
	s.t.Helper()
	p := filepath.Join(s.root, "proc", "mountinfo")
	old, _ := os.ReadFile(p)
	line := "25 1 " + majMin + " / " + at + " rw,noatime shared:1 - ext4 /dev/x rw\n"
	s.write("proc/mountinfo", string(old)+line)
}

func itoaU(n uint64) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}

func only(t *testing.T, got []Unusable) Unusable {
	t.Helper()
	if len(got) != 1 {
		t.Fatalf("want exactly one unusable drive, got %d: %+v", len(got), got)
	}
	return got[0]
}

// The case this whole feature exists for: a drive is plugged in, has nothing on
// it, and the program used to say no drives were plugged in.
func TestABlankDriveIsReportedWithItsOwnNameAndSize(t *testing.T) {
	s := newSysfs(t)
	usb := "sys/devices/platform/xhci/usb3/3-1"
	s.usbEnclosure(usb, "Ugreen Storage Device")
	s.disk("sda", usb+"/host0/target0/0:0:0:0", "8:0", 976773168, false)

	got := only(t, blockDevices())
	if got.Reason != ReasonBlank {
		t.Errorf("reason = %q, want %q", got.Reason, ReasonBlank)
	}
	if got.Name != "Ugreen Storage Device" {
		t.Errorf("name = %q, want the enclosure's own product name", got.Name)
	}
	if got.Device != "/dev/sda" {
		t.Errorf("device = %q, want /dev/sda", got.Device)
	}
	if got.Bus != "usb" {
		t.Errorf("bus = %q, want usb — a USB disk usually reports removable=0, so the bus is the only signal", got.Bus)
	}
	if want := "465.8GB"; !strings.Contains(got.Confirm, want) {
		t.Errorf("confirm = %q, want it to contain %q", got.Confirm, want)
	}
	if got.Detail == "" {
		t.Error("no explanation offered; the reason in words is the point of this type")
	}
}

// The system disk must never be offered for formatting. It is excluded because
// something is mounted on it, which is also what excludes a drive already in
// use as a destination.
func TestADiskWithAnythingMountedOnItIsNeverOffered(t *testing.T) {
	s := newSysfs(t)
	base := s.disk("mmcblk0", "sys/devices/platform/mmc/mmc0", "179:0", 62333952, false)
	s.partition(base, "mmcblk0p1", "179:1")
	s.partition(base, "mmcblk0p2", "179:2")
	s.mounted("179:2", "/")
	s.mounted("179:1", "/boot/firmware")

	if got := blockDevices(); len(got) != 0 {
		t.Fatalf("the running system's own disk was offered as spare storage: %+v", got)
	}
}

// A disk in use as swap appears nowhere in mountinfo, so without a separate
// check it would look blank.
func TestADiskInUseAsSwapIsNeverOffered(t *testing.T) {
	s := newSysfs(t)
	base := s.disk("sdb", "sys/devices/platform/xhci/usb3/4-1/host1", "8:16", 1048576, false)
	s.partition(base, "sdb1", "8:17")
	s.write("proc/swaps", "Filename\tType\tSize\tUsed\tPriority\n/dev/sdb1\tpartition\t524288\t0\t-2\n")

	if got := blockDevices(); len(got) != 0 {
		t.Fatalf("a disk being used as swap was offered as spare storage: %+v", got)
	}
}

func TestDiskStatesAreToldApart(t *testing.T) {
	for _, tc := range []struct {
		name       string
		partitions bool
		readOnly   bool
		want       string
	}{
		{"nothing on it", false, false, ReasonBlank},
		{"partitioned but not mounted", true, false, ReasonUnmounted},
		{"write protected", false, true, ReasonReadOnly},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newSysfs(t)
			base := s.disk("sdc", "sys/devices/platform/xhci/usb3/5-1/host2", "8:32", 2048000, tc.readOnly)
			if tc.partitions {
				s.partition(base, "sdc1", "8:33")
			}

			if got := only(t, blockDevices()); got.Reason != tc.want {
				t.Errorf("reason = %q, want %q", got.Reason, tc.want)
			}
		})
	}
}

func TestThingsThatAreNotDrivesAreIgnored(t *testing.T) {
	for _, tc := range []struct {
		name    string
		devPath string
		sectors uint64
	}{
		{"compressed swap in RAM", "sys/devices/virtual/block", 4194304},
		{"loopback file", "sys/devices/virtual/block", 4194304},
		{"empty card reader slot", "sys/devices/platform/xhci/usb3/6-1/host3", 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newSysfs(t)
			s.disk("sdz", tc.devPath, "7:0", tc.sectors, false)

			if got := blockDevices(); len(got) != 0 {
				t.Fatalf("%s was offered as a drive: %+v", tc.name, got)
			}
		})
	}
}

// Without a name from the bus, fall back to the SCSI fields — which the kernel
// chops at a fixed width, so they are joined rather than concatenated.
func TestADiskWithNoProductNameFallsBackToVendorAndModel(t *testing.T) {
	s := newSysfs(t)
	base := s.disk("sdd", "sys/devices/pci/ata1/host4", "8:48", 2048000, false)
	s.write(base+"/device/vendor", "ATA     \n")
	s.write(base+"/device/model", "Samsung SSD 870 \n")

	if got := only(t, blockDevices()); got.Name != "ATA Samsung SSD 870" {
		t.Errorf("name = %q, want vendor and model joined", got.Name)
	}
}

func TestMountPointsWithAwkwardNamesAreReadCorrectly(t *testing.T) {
	s := newSysfs(t)
	s.mounted("8:1", `/media/alex/My\040Backup\040Drive`)

	if got := MountPoints()["8:1"]; got != "/media/alex/My Backup Drive" {
		t.Errorf("mount point = %q, want the spaces unescaped", got)
	}
}
