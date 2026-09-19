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

// portalFSEmbed embeds the Vite build output. The portal/ subdirectory is a
// standalone npm project; run `npm --prefix portal install && npm --prefix
// portal run build` (or rely on the Dockerfile's portal stage) to populate
// dist/ before `go build`. The `all:` selector bundles the .gitkeep so the
// embed succeeds against a freshly-cloned tree with an otherwise empty dist/.
//
//go:embed all:portal/dist
var portalFSEmbed embed.FS

// portalFS returns the embedded bundle rooted at the dist directory, which is
// what provider-sdk/serve mounts: it owns the asset lookup, the Content-Type
// and the index.html fallback, so every provider's portal behaves the same.
func portalFS() (fs.FS, error) {
	return fs.Sub(portalFSEmbed, "portal/dist")
}
