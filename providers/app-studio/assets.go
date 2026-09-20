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
	"net/http"
)

// portalFS embeds the App Studio portal bundle. The checked-in .gitkeep keeps
// the embed target valid in a clean checkout; `make build-app-studio-provider`
// builds the real micro-frontend into portal/dist before compiling the provider.
//
//go:embed all:portal/dist
var portalFS embed.FS

// portalHandler returns the bundle as a handler and as the FS
// provider-sdk/serve mounts. serve.New owns the serving rules — a real file
// for an asset path, a 404 when the bundle has no such file (a retired lazy
// chunk must never come back as an HTML 200), and index.html for anything
// else so a direct visit to a client-side route shows the app.
func portalHandler() (http.Handler, fs.FS, error) {
	distFS, err := fs.Sub(portalFS, "portal/dist")
	if err != nil {
		return nil, nil, err
	}
	return http.FileServer(http.FS(distFS)), distFS, nil
}
