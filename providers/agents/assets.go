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

// portalAssets embeds the Vite build output. The portal/ subdirectory holds a
// standalone npm project; run `npm --prefix portal install && npm --prefix
// portal run build` to populate dist/ before `go build`.
//
// `all:` so dotfiles (.gitkeep) are bundled too — without it the embed fails
// at compile time when dist/ exists but is otherwise empty.
//
//go:embed all:portal/dist
var portalAssets embed.FS

// portalFS returns the embedded bundle for provider-sdk/serve to mount. The
// asset/index-fallback logic that used to live here is the SDK's now, so the
// two ends of the hub's UI proxy cannot drift on what counts as an asset path.
func portalFS() (fs.FS, error) {
	return fs.Sub(portalAssets, "portal/dist")
}
