// SPDX-License-Identifier: MIT

package syncthing

// PinnedVersion is the exact syncthing release this build of backup-maker
// installs and supervises. The SHA256 values below are taken from the official
// sha256sum.txt.asc of that release (signature verified by the developer at
// pin time), so runtime verification needs no PGP.
const PinnedVersion = "v2.1.2"

type archive struct {
	// URL filename suffix and archive kind for a platform.
	Name   string
	SHA256 string
	Zip    bool
}

// archives maps GOOS/GOARCH to the official release artifact. macOS ships a
// universal binary covering amd64 and arm64.
var archives = map[string]archive{
	"linux/amd64": {
		Name:   "syncthing-linux-amd64-" + PinnedVersion + ".tar.gz",
		SHA256: "0cea24660ac7d8df19f01da979827b5c9463a8a60042e296829293cf80213ee4",
	},
	"linux/arm64": {
		Name:   "syncthing-linux-arm64-" + PinnedVersion + ".tar.gz",
		SHA256: "2fcee9688f37df46337b0b78e7d2badc44549481e29eccaa8cdb1e698d79c8c5",
	},
	"windows/amd64": {
		Name:   "syncthing-windows-amd64-" + PinnedVersion + ".zip",
		SHA256: "4626c13012e9620ece2393bfc3300aeafead654695d5dc096a873c27a7543c96",
		Zip:    true,
	},
	"darwin/amd64": {
		Name:   "syncthing-macos-universal-" + PinnedVersion + ".zip",
		SHA256: "31ec0f7a58df841cfde5a69b00dd624cbc53400002c968ec789072cff83997b4",
		Zip:    true,
	},
	"darwin/arm64": {
		Name:   "syncthing-macos-universal-" + PinnedVersion + ".zip",
		SHA256: "31ec0f7a58df841cfde5a69b00dd624cbc53400002c968ec789072cff83997b4",
		Zip:    true,
	},
}

const releaseBaseURL = "https://github.com/syncthing/syncthing/releases/download/"
