// SPDX-License-Identifier: MIT

package archive

import (
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	zip "github.com/yeka/zip"

	"github.com/phil9922/backup-maker/internal/config"
	"github.com/phil9922/backup-maker/internal/localmirror"
)

func testSetup(t *testing.T) (*config.Config, config.Archive, localmirror.Backend, string) {
	t.Helper()
	src := t.TempDir()
	dst := t.TempDir()
	if err := os.MkdirAll(filepath.Join(src, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(src, "a.txt"), []byte("alpha"), 0o644)
	os.WriteFile(filepath.Join(src, "sub", "b.txt"), []byte("beta"), 0o644)
	os.MkdirAll(filepath.Join(src, "node_modules"), 0o755)
	os.WriteFile(filepath.Join(src, "node_modules", "junk.js"), []byte("x"), 0o644)

	cfg := config.New()
	cfg.General.MachineName = "mach"
	cfg.Folders = []config.Folder{{ID: "f1", Path: src, Label: "proj"}}
	job := config.Archive{Name: "testjob", Every: "daily", Target: "t", Keep: 2}
	return cfg, job, localmirror.NewLocalFS(dst), dst
}

func TestRunRoundTrip(t *testing.T) {
	cfg, job, b, dst := testSetup(t)
	log := slog.New(slog.DiscardHandler)

	res := Run(b, cfg, job, "hunter2", log)
	if res.Err != "" {
		t.Fatalf("run failed: %s", res.Err)
	}
	if res.Files != 2 {
		t.Errorf("archived %d files, want 2 (node_modules must be excluded)", res.Files)
	}

	// The zip exists, decrypts with the right password, and contents match.
	full := filepath.Join(dst, filepath.FromSlash(res.File))
	zr, err := zip.OpenReader(full)
	if err != nil {
		t.Fatal(err)
	}
	defer zr.Close()
	got := map[string]string{}
	for _, zf := range zr.File {
		if !zf.IsEncrypted() {
			t.Errorf("entry %s is NOT encrypted", zf.Name)
		}
		zf.SetPassword("hunter2")
		rc, err := zf.Open()
		if err != nil {
			t.Fatalf("open %s: %v", zf.Name, err)
		}
		data, _ := io.ReadAll(rc)
		rc.Close()
		got[zf.Name] = string(data)
	}
	if got["proj/a.txt"] != "alpha" || got["proj/sub/b.txt"] != "beta" {
		t.Errorf("contents wrong: %v", got)
	}

	// Wrong password must not decrypt.
	zr2, err := zip.OpenReader(full)
	if err != nil {
		t.Fatal(err)
	}
	defer zr2.Close()
	zr2.File[0].SetPassword("wrong")
	rc, err := zr2.File[0].Open()
	if err == nil {
		if _, err = io.ReadAll(rc); err == nil {
			t.Error("decryption succeeded with the wrong password")
		}
		rc.Close()
	}
}

func TestRunRequiresPassword(t *testing.T) {
	cfg, job, b, _ := testSetup(t)
	res := Run(b, cfg, job, "", slog.New(slog.DiscardHandler))
	if res.Err == "" {
		t.Fatal("expected refusal without a password")
	}
}

func TestRetention(t *testing.T) {
	cfg, job, b, dst := testSetup(t)
	log := slog.New(slog.DiscardHandler)
	// keep=2: after three runs only two zips remain. Stamps have 1s
	// resolution, so name the extra run manually via a fake old file.
	dir := filepath.Join(dst, DirName, "mach", "testjob")
	os.MkdirAll(dir, 0o755)
	os.WriteFile(filepath.Join(dir, "testjob-20200101-000000.zip"), []byte("old"), 0o644)
	os.WriteFile(filepath.Join(dir, "testjob-20200102-000000.zip"), []byte("old"), 0o644)

	res := Run(b, cfg, job, "pw", log)
	if res.Err != "" {
		t.Fatal(res.Err)
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 2 {
		names := []string{}
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Errorf("retention kept %d files, want 2: %v", len(entries), names)
	}
	// The newest (the real archive we just wrote) must have survived.
	if _, err := os.Stat(filepath.Join(dst, filepath.FromSlash(res.File))); err != nil {
		t.Error("newest archive was pruned")
	}
}

// The exclude list is shared with the live mirror, so this flag is the only
// way to keep a lean mirror on a small card while still sealing a complete
// archive on a bigger drive. Prove node_modules really lands in the zip.
func TestIncludeEverythingSealsIgnoredFiles(t *testing.T) {
	cfg, job, b, _ := testSetup(t)
	log := slog.New(slog.DiscardHandler)

	// Default: node_modules is skipped, matching the mirror.
	lean := Run(b, cfg, job, "pw", log)
	if lean.Err != "" {
		t.Fatalf("lean run failed: %s", lean.Err)
	}
	if lean.Files != 2 {
		t.Fatalf("lean snapshot has %d files, want 2 (a.txt and sub/b.txt)", lean.Files)
	}

	// Opted in: the junk is sealed too.
	job.Name = "fatjob"
	job.NoDefaultIgnores = true
	fat := Run(b, cfg, job, "pw", log)
	if fat.Err != "" {
		t.Fatalf("fat run failed: %s", fat.Err)
	}
	if fat.Files != 3 {
		t.Errorf("fat snapshot has %d files, want 3 (node_modules/junk.js included)", fat.Files)
	}
}

// A snapshot-only exclusion must not need changing the folder, or the mirror
// would be affected too.
func TestArchiveExtraIgnoreAppliesToSnapshotOnly(t *testing.T) {
	cfg, job, b, _ := testSetup(t)
	job.ExtraIgnore = []string{"sub"}

	res := Run(b, cfg, job, "pw", slog.New(slog.DiscardHandler))
	if res.Err != "" {
		t.Fatalf("run failed: %s", res.Err)
	}
	if res.Files != 1 {
		t.Errorf("snapshot has %d files, want 1 (sub/ excluded for this job only)", res.Files)
	}
	if len(cfg.Folders[0].ExtraIgnore) != 0 {
		t.Error("the folder's own excludes were modified; the mirror would change too")
	}
}
