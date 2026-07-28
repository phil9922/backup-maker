// SPDX-License-Identifier: MIT

package main

import (
	"embed"

	"github.com/phil9922/backup-maker/internal/webui"
)

// docsFS carries the documentation into the binary, so it can be read on a
// machine with no internet connection — which is exactly the machine most
// likely to need it. A backup destination in a cupboard, a Pi on a network
// with no route out, a laptop somewhere with no signal: the docs are there
// regardless, at http://127.0.0.1:8666/docs.
//
// THE EMBED DIRECTIVE HAS TO LIVE AT THE REPOSITORY ROOT. go:embed cannot
// reference a path outside its own package directory, so `internal/webui`
// physically cannot embed `docs/` no matter how much more natural that would
// be. The alternatives were worse: a generated copy of every page checked into
// the package (clutter, and it goes stale silently), or a build step that
// goreleaser would have to run before compiling. This file is three lines and
// obvious.
//
//go:embed docs
var docsFS embed.FS

func init() { webui.SetDocs(docsFS) }
