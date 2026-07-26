// SPDX-License-Identifier: MIT

package browse

import (
	"os"
	"os/user"
	"path/filepath"
)

// mountParents are the directories under which desktop Linux mounts removable
// media. /media/<user> and /run/media/<user> are what udisks2 uses; /mnt is
// where hand-mounted disks conventionally live.
func mountParents() []string {
	var dirs []string
	name := ""
	if u, err := user.Current(); err == nil {
		name = u.Username
	} else {
		name = os.Getenv("USER")
	}
	if name != "" {
		dirs = append(dirs,
			filepath.Join("/media", name),
			filepath.Join("/run/media", name),
		)
	}
	return append(dirs, "/media", "/mnt")
}
