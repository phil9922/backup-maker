// SPDX-License-Identifier: MIT

package notify

import (
	"os"
	"path/filepath"
	"testing"
)

// The icon ships inside the binary but notify-send needs a path, so it has to
// reach the disk before the first notification — and be a real PNG when it
// gets there, or the notification daemon draws nothing.
func TestTheNotificationIconIsUnpackedToARealFile(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_DATA_HOME", dir)
	// The path is resolved once per process, so a test that runs after another
	// has already resolved it would assert about the wrong directory.
	iconOnce = onceReset()
	iconFile = ""

	path := IconPath()
	if path == "" {
		t.Fatal("no icon path: notifications would be raised without one")
	}
	if want := filepath.Join(dir, "backup-maker", "backup-maker.png"); path != want {
		t.Errorf("icon written to %s, want %s", path, want)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("the icon was not actually written: %v", err)
	}
	if len(data) < 8 || string(data[1:4]) != "PNG" {
		t.Errorf("the file is not a PNG (%d bytes)", len(data))
	}
	// Nothing left behind from the atomic write.
	if _, err := os.Stat(path + ".tmp"); !os.IsNotExist(err) {
		t.Error("a temp file was left beside the icon")
	}
}

// A machine where the icon cannot be placed must still be able to alert. This
// is a backup program: a notification with no picture is a small
// disappointment, one that never fires because a picture failed is not.
func TestAnUnwritableIconDirectoryDoesNotBreakAlerting(t *testing.T) {
	dir := t.TempDir()
	blocked := filepath.Join(dir, "blocked")
	if err := os.WriteFile(blocked, []byte("not a directory"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XDG_DATA_HOME", blocked) // MkdirAll under a file must fail
	iconOnce = onceReset()
	iconFile = ""

	if path := IconPath(); path != "" {
		t.Errorf("expected no icon path when it cannot be written, got %q", path)
	}
	// The caller's contract: "" means "send it without an icon", never a crash.
}
