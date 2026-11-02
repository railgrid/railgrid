// Copyright 2026 The Railgrid Authors.
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy at http://www.apache.org/licenses/LICENSE-2.0

// Command provider-sandbox-smoke serves the browser fixture with the real Hub
// portal CSP middleware and proxies Vite's TS module transform on the same host.
package main

import (
	"log"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"path/filepath"

	"github.com/railgrid/railgrid/pkg/hub"
)

func main() {
	root, err := os.Getwd()
	if err != nil {
		log.Fatal(err)
	}
	viteURL, err := url.Parse("http://127.0.0.1:5174")
	if err != nil {
		log.Fatal(err)
	}
	vite := httputil.NewSingleHostReverseProxy(viteURL)
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/":
			http.ServeFile(w, r, filepath.Join(root, "hack/provider-sandbox-smoke/index.html"))
		case "/ui/provider-sandbox.html":
			http.ServeFile(w, r, filepath.Join(root, "portal/public/provider-sandbox.html"))
		case "/ui/provider-sandbox-bootstrap.js":
			http.ServeFile(w, r, filepath.Join(root, "portal/public/provider-sandbox-bootstrap.js"))
		default:
			vite.ServeHTTP(w, r)
		}
	})
	log.Print("Open http://127.0.0.1:4173/ in Safari or another browser")
	log.Fatal(http.ListenAndServe("127.0.0.1:4173", hub.WithPortalSecurityHeadersForHub(mux, "http://127.0.0.1:4173")))
}
