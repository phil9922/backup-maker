// SPDX-License-Identifier: MIT

package notify

import (
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"os"
	"path/filepath"
	"sync"
)

//go:embed icons/backup-maker.png
var iconFS embed.FS

// iconPath is resolved once. A notification is best-effort and frequent enough
// that it must not stat the filesystem every time.
var (
	iconOnce sync.Once
	iconFile string
)

// IconPath returns a path to backup-maker's notification icon on disk, writing
// it out on first use, or "" if it cannot be placed anywhere.
//
// WHY IT HAS TO BE ON DISK. The icon travels inside the binary — that is what
// makes backup-maker a single file to install — but notify-send takes a PATH,
// and hands it to a notification daemon in another process that will open it
// itself. Nothing embedded can satisfy that, so the icon is unpacked once into
// the user's data directory and referenced from there.
//
// It goes in XDG_DATA_HOME rather than the config directory deliberately: the
// config directory is excluded from every backup by path (config.SelfExcludes),
// and quietly relying on that for an unrelated file would make the exclusion
// harder to reason about later. An icon is not a secret.
//
// Failure is silent and returns "": a notification with no icon is a small
// disappointment, and a backup daemon that could not raise an alert because it
// could not write a picture would be a serious one.
func IconPath() string {
	iconOnce.Do(func() {
		data, err := iconFS.ReadFile("icons/backup-maker.png")
		if err != nil {
			return
		}
		dir, err := iconDir()
		if err != nil {
			return
		}
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return
		}
		path := filepath.Join(dir, "backup-maker.png")
		// Rewritten only when it differs, so a new build's icon replaces the
		// old one while an unchanged one costs a hash and nothing else.
		if existing, err := os.ReadFile(path); err == nil && sameBytes(existing, data) {
			iconFile = path
			return
		}
		tmp := path + ".tmp"
		if err := os.WriteFile(tmp, data, 0o644); err != nil {
			return
		}
		if err := os.Rename(tmp, path); err != nil {
			_ = os.Remove(tmp)
			return
		}
		iconFile = path
	})
	return iconFile
}

// onceReset gives the tests a fresh resolution. IconPath deliberately resolves
// once per process, which is right in production and useless in a test binary
// that needs to try more than one environment.
func onceReset() sync.Once { return sync.Once{} }

func sameBytes(a, b []byte) bool {
	ha, hb := sha256.Sum256(a), sha256.Sum256(b)
	return hex.EncodeToString(ha[:]) == hex.EncodeToString(hb[:])
}

// iconDir follows the XDG data convention on Linux and falls back to the
// user's config directory elsewhere, which is where Go already puts per-user
// application data on macOS and Windows.
func iconDir() (string, error) {
	if d := os.Getenv("XDG_DATA_HOME"); d != "" {
		return filepath.Join(d, "backup-maker"), nil
	}
	home, err := os.UserHomeDir()
	if err == nil {
		return filepath.Join(home, ".local", "share", "backup-maker"), nil
	}
	cfg, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(cfg, "backup-maker"), nil
}
