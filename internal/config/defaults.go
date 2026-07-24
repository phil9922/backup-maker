// SPDX-License-Identifier: MIT

package config

// DefaultIgnores are junk patterns excluded from backups unless a folder opts
// out. Deliberately absent: .git — losing repositories is unacceptable.
var DefaultIgnores = []string{
	"node_modules",
	"__pycache__",
	".venv",
	"venv",
	".tox",
	"target/debug",
	"target/release",
	"build",
	"dist",
	".next",
	".gradle",
	"*.pyc",
	".DS_Store",
	"Thumbs.db",
	".cache",
}

const (
	DefaultDashboardPort     = 8666
	DefaultLANViewPort       = 8667
	DefaultVersioningMaxDays = 30
	DefaultStaleAfterDays    = 7
)
