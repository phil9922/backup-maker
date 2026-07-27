// SPDX-License-Identifier: MIT

package browse

// mountParents: macOS mounts every external volume under /Volumes (the boot
// disk appears there too, as a symlink to /).
func mountParents() []string { return []string{"/Volumes"} }
