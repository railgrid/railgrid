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
	"errors"
	"io"
	"io/fs"
	"log"
	"mime"
	"net/http"
	"path"
	"strings"
)

//go:embed all:portal/dist
var portalFS embed.FS

func portalHandler() (http.Handler, fs.FS, error) {
	distFS, err := fs.Sub(portalFS, "portal/dist")
	if err != nil {
		return nil, nil, err
	}
	return http.FileServer(http.FS(distFS)), distFS, nil
}

func servePortalAsset(w http.ResponseWriter, _ *http.Request, distFS fs.FS, name string) bool {
	name = strings.TrimPrefix(name, "/")
	if name == "" {
		return false
	}
	f, err := distFS.Open(name)
	if err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			log.Printf("portal asset %s: %v", name, err)
		}
		return false
	}
	defer f.Close()

	ct := mime.TypeByExtension(path.Ext(name))
	if ct == "" {
		ct = "application/octet-stream"
	}
	w.Header().Set("Content-Type", ct)
	w.Header().Set("Cache-Control", "no-cache")
	if _, err := io.Copy(w, f); err != nil {
		log.Printf("portal asset %s write: %v", name, err)
	}
	return true
}
