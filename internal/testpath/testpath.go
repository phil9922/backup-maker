// SPDX-License-Identifier: MIT

// Package testpath makes a test fixture's paths absolute on the platform the
// test is actually running on.
//
// WHY IT EXISTS. Tests here were written on Linux and name paths the way Linux
// does — "/mnt/card", "/home/old/docs". Those are not absolute paths on Windows,
// so `config.Validate` rejected fifty-seven tests' fixtures the first time the
// suite was run there: `folder "f1" path must be absolute: /tmp/src`. The check
// was right and the fixtures were wrong.
//
// The alternative was weakening the absolute-path check, which is not on the
// table: it is what stops a hand-edited config.toml from aiming a backup — or a
// deletion — at a relative path resolved against whatever directory the daemon
// happened to start in.
//
// A REAL PACKAGE RATHER THAN A HELPER IN EACH TEST FILE, because a dozen
// packages need it and a copy per package is a dozen places to get the Windows
// spelling subtly different. It contains no production logic and nothing imports
// it outside tests, so it is compiled out of the shipped binary.
package testpath

import (
	"path/filepath"
	"runtime"
)

// Abs makes a POSIX-looking fixture path absolute on this platform, and leaves
// it exactly as written everywhere except Windows.
//
// Fixtures keep saying what they mean — "/mnt/card" reads as a mount point,
// which "C:\\mnt\\card" does not — and become real absolute paths where the
// distinction matters. It deliberately does NOT create anything: many of these
// fixtures name storage that is meant to be absent.
//
// The drive letter is fixed rather than read from the environment. A fixture
// path is a name, not a location: nothing is written to it, and a test comparing
// two of them needs them spelled the same way every time.
func Abs(p string) string {
	if runtime.GOOS != "windows" {
		return p
	}
	return filepath.Join(`C:\`, filepath.FromSlash(p))
}

// Slash is Abs for a path that a test then compares against a SLASH-separated
// value — a destination path inside a backup, which this program keeps
// slash-separated on every platform (see config.DestRoot).
//
// Separate from Abs because the two are not interchangeable and the failure is
// quiet: a test that compares a backslash path against what the mirror engine
// recorded passes on Linux and fails on Windows for a reason that looks like a
// bug in the engine.
func Slash(p string) string {
	return filepath.ToSlash(Abs(p))
}
