// Copyright 2026 The Faros Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package kubernetesedges

import (
	"embed"
	"io/fs"
)

// portalFS embeds the Vite-built kubernetes-edges micro-frontend the
// hub serves under /ui/providers/kubernetes-edges/. The bundle holds
// main.js (custom-element shell + the four page components + their
// shared portal dependencies pulled in via Vite alias). Build pipeline:
// `make build-kubernetes-edges-provider-portal` runs Vite; build-hub
// depends on that target.
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
