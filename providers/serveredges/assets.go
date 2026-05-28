// Copyright 2026 The Faros Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package serveredges

import (
	"embed"
	"io/fs"
)

// portalFS embeds the Vite-built server-edges micro-frontend the hub
// serves under /ui/providers/server-edges/. The bundle imports the
// shared edge primitives (EdgesPage, EdgeDetailPage, EdgeCreateModal,
// TerminalPage) from kubernetes-edges' source tree via Vite alias
// during build, so this single main.js is everything the host portal
// needs to render the server-edges micro-frontend.
//
//go:embed all:portal/dist
var portalFS embed.FS

func localUIAssets() fs.FS {
	sub, err := fs.Sub(portalFS, "portal/dist")
	if err != nil {
		panic(err)
	}
	return sub
}
