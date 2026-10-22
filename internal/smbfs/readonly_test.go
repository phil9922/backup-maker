// SPDX-License-Identifier: MIT

package smbfs

import (
	"testing"

	smb2 "github.com/cloudsoda/go-smb2"

	"github.com/phil9922/backup-maker/internal/localmirror"
)

// Samba's answer for a disk the kernel has remounted read-only. It must reach
// the mirror as a destination that refuses writes, not as one file failing —
// which is how three weeks of refused writes to a Pi read as "backed up".
func TestAWriteProtectedShareIsReportedAsReadOnly(t *testing.T) {
	err := (&FS{}).opErr(&smb2.ResponseError{Code: ntMediaWriteProtected})
	if !localmirror.IsReadOnly(err) {
		t.Fatalf("STATUS_MEDIA_WRITE_PROTECTED came back as %v, not a read-only destination", err)
	}
	if localmirror.IsNoSpace(err) {
		t.Fatal("a read-only share was reported as full, which would start deleting backup history")
	}
}
