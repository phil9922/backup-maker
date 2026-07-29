// SPDX-License-Identifier: MIT

package daemon

import "fmt"

// releasesBase is where the published binaries live.
const releasesBase = "https://github.com/phil9922/backup-maker/releases"

// recipe is one platform's worth of copy-paste setup for another computer.
type recipe struct {
	ID       string   `json:"id"`
	Label    string   `json:"label"`
	Commands []string `json:"commands"`
	Note     string   `json:"note,omitempty"`
}

// installRecipes writes out the commands for putting backup-maker on another
// computer, one set per platform.
//
// The platform cannot be detected. A machine found on the network answers as a
// file server and says nothing about its processor, so asking the person who
// owns it — who knows — beats guessing on their behalf and handing them a
// binary that will not run.
//
// The version is pinned to the one this dashboard is running, so two machines
// on a network end up on the same build instead of whatever was newest on the
// day each was set up.
func installRecipes(version string) []recipe {
	linger := "sudo loginctl enable-linger $USER"
	out := []recipe{
		{
			ID:       "linux_arm64",
			Label:    "Raspberry Pi 4, 5 or other 64-bit ARM Linux",
			Commands: unixInstall(version, "linux_arm64", "tar.gz", "~/.local/bin", linger),
			Note:     "The last line is what keeps backups running after you close the SSH session. On a machine with no screen, without it the service stops when you log out.",
		},
		{
			ID:       "linux_amd64",
			Label:    "Linux on an Intel or AMD computer",
			Commands: unixInstall(version, "linux_amd64", "tar.gz", "~/.local/bin", linger),
			Note:     "The last line matters on a machine you log into remotely; on a desktop you sit at, it changes nothing.",
		},
		{
			ID:       "linux_armv7",
			Label:    "Older 32-bit Raspberry Pi (Pi 2, Pi 3, Zero 2 W)",
			Commands: unixInstall(version, "linux_armv7", "tar.gz", "~/.local/bin", linger),
			Note:     "Use this if `uname -m` on that machine says armv7l rather than aarch64.",
		},
		{
			ID:       "darwin_arm64",
			Label:    "Mac with Apple silicon",
			Commands: unixInstall(version, "darwin_arm64", "tar.gz", "/usr/local/bin", ""),
		},
		{
			ID:       "darwin_amd64",
			Label:    "Mac with an Intel processor",
			Commands: unixInstall(version, "darwin_amd64", "tar.gz", "/usr/local/bin", ""),
		},
		{
			ID:       "windows_amd64",
			Label:    "Windows",
			Commands: windowsInstall(version),
			Note:     "Run these in PowerShell, in the folder you unzipped it into.",
		},
	}
	return out
}

// unixInstall is the same steps everywhere: fetch, put it somewhere it will
// stay, configure, keep it running.
func unixInstall(version, platform, ext, binDir, linger string) []string {
	cmds := append([]string{}, fetchLines(version, platform, ext)...)
	cmds = append(cmds,
		fmt.Sprintf("mkdir -p %s && install -m 755 backup-maker %s/backup-maker", binDir, binDir),
		binDir+"/backup-maker init",
		binDir+"/backup-maker autostart enable")
	if linger != "" {
		cmds = append(cmds, linger)
	}
	return cmds
}

func windowsInstall(version string) []string {
	where := releasesBase + "/latest"
	if version != "" {
		where = assetURL(version, "windows_amd64", "zip")
	}
	return []string{
		"# Download and unzip:",
		"#   " + where,
		"# then, in the folder you unzipped it into:",
		".\\backup-maker.exe init",
		".\\backup-maker.exe autostart enable",
	}
}

// fetchLines gets the binary onto the other machine.
//
// With no version to pin there is no direct download to give, and a one-liner
// with the explanation wedged into it is worse than no one-liner: a "#" turns
// the rest of the line into a comment, so the pipe that does the actual work
// silently disappears. Unreleased builds therefore get instructions that are
// honest about being two steps.
func fetchLines(version, platform, ext string) []string {
	if version == "" {
		return []string{
			"# This dashboard is running an unreleased build, so there is no",
			"# matching download to link to. Get the " + platform + " archive from:",
			"#   " + releasesBase + "/latest",
			"# then, in the folder you downloaded it to:",
			"tar -xzf backup-maker_*_" + platform + "." + ext + " backup-maker",
		}
	}
	return []string{"curl -fsSL " + assetURL(version, platform, ext) + " | tar -xz backup-maker"}
}

// assetURL names the published archive. Only ever called with a real released
// version — see fetchLines.
func assetURL(version, platform, ext string) string {
	return fmt.Sprintf("%s/download/v%s/backup-maker_%s_%s.%s", releasesBase, version, version, platform, ext)
}
