// Copyright 2026 The Railgrid Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package main

import (
	"embed"
	"io/fs"
)

// portalFS embeds the Vite build output. The portal/ subdirectory holds a
// standalone npm project (Vite + TypeScript); see portal/README.md.
//
// `all:` so dotfiles (.gitkeep) are bundled too — without that the embed
// would fail at compile time when the dist/ directory exists but is empty.
// Run `npm --prefix portal install && npm --prefix portal run build` (or
// `make build-quickstart-provider` from the repo root) to populate dist/
// before `go build`; the Makefile target chains the two.
//
//go:embed all:portal/dist
var portalFS embed.FS

// portalDist returns the embedded Vite build output rooted at the dist
// directory, for provider-sdk/serve to file-serve with an index fallback.
func portalDist() (fs.FS, error) {
	return fs.Sub(portalFS, "portal/dist")
}
