// Copyright 2026 The Faros Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package mcp

import (
	"embed"
	"io/fs"
)

// portalFS embeds the Vite-built micro-frontend the hub serves under
// /ui/providers/mcp/. The bundle holds main.js (custom-element shell +
// MCPHost + MCPPage + MCPDetailPage + the shared portal components
// they depend on, via Vite alias bundling), an optional icon.svg, and
// an index.html fallback. Built by `make build-mcp-provider-portal`
// (which the hub binary build depends on).
//
// `all:` so dotfiles (.gitkeep, used to keep the dir present before a
// first build) are embedded too — without it `go build` errors when
// dist/ is empty.
//
//go:embed all:portal/dist
var portalFS embed.FS

// localUIAssets returns the embedded portal/dist subtree as an fs.FS
// rooted at the dist directory, so consumers see "main.js" rather than
// "portal/dist/main.js".
func localUIAssets() fs.FS {
	sub, err := fs.Sub(portalFS, "portal/dist")
	if err != nil {
		// fs.Sub only fails on malformed embed; would be caught at
		// compile or first init().
		panic(err)
	}
	return sub
}
