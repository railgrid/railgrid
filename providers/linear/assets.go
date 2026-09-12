// Copyright 2026 The Faros Authors.
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

//go:embed all:portal/dist
var portalFS embed.FS

func portalHandler() (http.Handler, fs.FS, error) {
	f, err := fs.Sub(portalFS, "portal/dist")
	if err != nil {
		return nil, nil, err
	}
	return http.FileServer(http.FS(f)), f, nil
}
