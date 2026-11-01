// SPDX-License-Identifier: MIT

//go:build !windows

package archive

import "os"

// markSparse is a no-op: every other filesystem the suite runs on leaves holes
// in a truncated file without being asked.
func markSparse(*os.File) error { return nil }
