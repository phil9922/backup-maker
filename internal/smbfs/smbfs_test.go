// SPDX-License-Identifier: MIT

package smbfs

import (
	"os"
	"testing"

	"github.com/phil9922/backup-maker/internal/localmirror"
)

func TestParse(t *testing.T) {
	cases := []struct {
		in                         string
		host, port, share, subpath string
		wantErr                    bool
	}{
		{in: "//nas/backup", host: "nas", port: "445", share: "backup"},
		{in: "//192.168.1.42/media/backups/laptop", host: "192.168.1.42", port: "445", share: "media", subpath: "backups/laptop"},
		{in: "//127.0.0.1:4455/share", host: "127.0.0.1", port: "4455", share: "share"},
		{in: `\\OTHER-PC\Backups`, host: "OTHER-PC", port: "445", share: "Backups"},
		{in: "smb://router/usb1", host: "router", port: "445", share: "usb1"},
		{in: "//justhost", wantErr: true},
		{in: "nonsense", wantErr: true},
	}
	for _, c := range cases {
		host, port, share, subpath, err := Parse(c.in)
		if c.wantErr {
			if err == nil {
				t.Errorf("Parse(%q): expected error", c.in)
			}
			continue
		}
		if err != nil {
			t.Errorf("Parse(%q): %v", c.in, err)
			continue
		}
		if host != c.host || port != c.port || share != c.share || subpath != c.subpath {
			t.Errorf("Parse(%q) = %s %s %s %s", c.in, host, port, share, subpath)
		}
	}
}

// TestSMBConformance runs the shared backend suite against a real SMB server.
// Skipped unless BM_SMB_TEST_URL (+_USER/_PASS) is set, e.g.:
//
//	impacket-smbserver -smb2support -port 4455 -username u -password p share /tmp/smbtest
//	BM_SMB_TEST_URL=//127.0.0.1:4455/share BM_SMB_TEST_USER=u BM_SMB_TEST_PASS=p go test ./internal/smbfs
func TestSMBConformance(t *testing.T) {
	url := os.Getenv("BM_SMB_TEST_URL")
	if url == "" {
		t.Skip("BM_SMB_TEST_URL not set")
	}
	f, err := New(url, os.Getenv("BM_SMB_TEST_USER"), os.Getenv("BM_SMB_TEST_PASS"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	localmirror.ExerciseBackend(t, f)
}
