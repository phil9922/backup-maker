// SPDX-License-Identifier: MIT

package syncthing

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/phil9922/backup-maker/internal/config"
)

// BinaryPath returns where the pinned syncthing binary lives (it may not
// exist yet).
func BinaryPath() (string, error) {
	dir, err := config.Dir()
	if err != nil {
		return "", err
	}
	name := "syncthing"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	return filepath.Join(dir, "bin", "syncthing-"+PinnedVersion, name), nil
}

// Ensure makes the pinned syncthing binary available, downloading and
// verifying it on first run. Returns its path.
func Ensure(log *slog.Logger) (string, error) {
	path, err := BinaryPath()
	if err != nil {
		return "", err
	}
	if _, err := os.Stat(path); err == nil {
		return path, nil
	}

	key := runtime.GOOS + "/" + runtime.GOARCH
	art, ok := archives[key]
	if !ok {
		return "", fmt.Errorf("no pinned syncthing build for %s; install syncthing yourself and re-run", key)
	}

	log.Info("downloading sync engine", "version", PinnedVersion, "artifact", art.Name)
	tmp, err := download(releaseBaseURL+PinnedVersion+"/"+art.Name, art.SHA256)
	if err != nil {
		if sys, sysErr := systemSyncthing(); sysErr == nil {
			log.Warn("download failed; falling back to system syncthing", "err", err, "path", sys)
			return sys, nil
		}
		return "", fmt.Errorf("downloading syncthing: %w (no usable system syncthing found either)", err)
	}
	defer os.Remove(tmp)

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return "", err
	}
	if art.Zip {
		err = extractZip(tmp, path)
	} else {
		err = extractTarGz(tmp, path)
	}
	if err != nil {
		return "", fmt.Errorf("extracting %s: %w", art.Name, err)
	}
	log.Info("sync engine installed", "path", path)
	return path, nil
}

// download fetches url to a temp file and verifies its SHA256.
func download(url, wantSHA string) (string, error) {
	client := &http.Client{Timeout: 10 * time.Minute}
	resp, err := client.Get(url)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("GET %s: %s", url, resp.Status)
	}

	f, err := os.CreateTemp("", "backup-maker-syncthing-*")
	if err != nil {
		return "", err
	}
	h := sha256.New()
	_, err = io.Copy(io.MultiWriter(f, h), resp.Body)
	closeErr := f.Close()
	if err != nil || closeErr != nil {
		os.Remove(f.Name())
		return "", fmt.Errorf("saving download: %w", errFirst(err, closeErr))
	}
	got := hex.EncodeToString(h.Sum(nil))
	if got != wantSHA {
		os.Remove(f.Name())
		return "", fmt.Errorf("SHA256 mismatch for %s: got %s want %s", url, got, wantSHA)
	}
	return f.Name(), nil
}

func errFirst(errs ...error) error {
	for _, e := range errs {
		if e != nil {
			return e
		}
	}
	return nil
}

// wantedBinary reports whether an archive member is the real syncthing
// binary. Releases also ship rc scripts named "syncthing" under etc/, so only
// the top-level "<release-dir>/syncthing(.exe)" entry qualifies.
func wantedBinary(name string) bool {
	parts := strings.Split(strings.Trim(filepath.ToSlash(name), "/"), "/")
	if len(parts) != 2 {
		return false
	}
	return parts[1] == "syncthing" || parts[1] == "syncthing.exe"
}

func extractTarGz(archivePath, dest string) error {
	f, err := os.Open(archivePath)
	if err != nil {
		return err
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return err
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		if hdr.Typeflag == tar.TypeReg && wantedBinary(hdr.Name) {
			return writeBinary(dest, tr)
		}
	}
	return fmt.Errorf("syncthing binary not found in archive")
}

func extractZip(archivePath, dest string) error {
	zr, err := zip.OpenReader(archivePath)
	if err != nil {
		return err
	}
	defer zr.Close()
	for _, zf := range zr.File {
		if zf.FileInfo().Mode().IsRegular() && wantedBinary(zf.Name) {
			rc, err := zf.Open()
			if err != nil {
				return err
			}
			defer rc.Close()
			return writeBinary(dest, rc)
		}
	}
	return fmt.Errorf("syncthing binary not found in archive")
}

func writeBinary(dest string, r io.Reader) error {
	tmp := dest + ".tmp"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755)
	if err != nil {
		return err
	}
	if _, err := io.Copy(f, r); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, dest)
}

// systemSyncthing finds a syncthing on PATH as an offline fallback.
func systemSyncthing() (string, error) {
	path, err := exec.LookPath("syncthing")
	if err != nil {
		return "", err
	}
	out, err := exec.Command(path, "--version").Output()
	if err != nil {
		return "", err
	}
	if !strings.Contains(string(out), "syncthing") {
		return "", fmt.Errorf("unexpected --version output from %s", path)
	}
	return path, nil
}
