// SPDX-License-Identifier: MIT

package browse

// ListUnusable reports storage that is present but cannot hold backups yet.
//
// Windows assigns a drive letter to any volume it can mount, and Drives()
// already lists those. A disk with no filesystem gets no letter and is not
// reachable through the API this package uses, so there is nothing truthful to
// report here yet: Disk Management is where that disk gets initialised.
// Returning nothing is better than guessing.
func ListUnusable() []Unusable { return nil }
