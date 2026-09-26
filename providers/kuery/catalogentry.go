// Copyright 2026 The Railgrid Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package main

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"

	"github.com/railgrid/provider-sdk/serve"
)

// The CatalogEntry manifest is this provider's single declaration of what it
// serves: `init` applies it into the provider workspace, apiexportgen turns the
// same document into the APIExport's "<resource>/<verb>" entries, and serve
// derives from it the routes it answers on the path a kcp shard forwards for a
// custom subresource. Both subcommands run from the same image with the same
// env, so they must resolve it the same way — hence one lookup, here.

// catalogEntryFileName is the name the chart's ConfigMap gives the manifest,
// and the name to look for when only the kcp object directory is pointed at.
const catalogEntryFileName = "catalogentry.yaml"

// catalogEntryPath returns the CatalogEntry manifest this image ships, or ""
// when none is mounted.
//
// RAILGRID_CATALOGENTRY_FILE is the explicit path (the chart renders the entry
// into a ConfigMap and mounts it at /etc/railgrid/catalogentry). Failing that,
// RAILGRID_KCP_DIR is searched for the same file name, for an image that bakes
// it beside the other kcp objects.
//
// A path derived from RAILGRID_KCP_DIR is returned only when the file is
// actually there: init treats a non-empty path as "apply this", and a
// deployment that mounts no entry must keep skipping it rather than start
// failing.
func catalogEntryPath() string {
	if p := strings.TrimSpace(os.Getenv("RAILGRID_CATALOGENTRY_FILE")); p != "" {
		return p
	}
	dir := strings.TrimSpace(os.Getenv("RAILGRID_KCP_DIR"))
	if dir == "" {
		return ""
	}
	p := filepath.Join(dir, catalogEntryFileName)
	if _, err := os.Stat(p); err != nil {
		return ""
	}
	return p
}

// subresourceRoutes derives serve.Options.Subresources from the CatalogEntry
// manifest, so the coordinates this provider answers on the shard-forwarded
// path can never drift from the ones it declares.
//
// The manifest is not optional: the kube path is the ONLY way a verb is
// reached, so a process with no table has no data plane at all, and serve.New
// refuses to build one anyway. A dev or test invocation points
// RAILGRID_CATALOGENTRY_FILE at providers/kuery/manifest.yaml (the Makefile's
// run-provider-kuery does). An unreadable or invalid manifest is equally an
// error, because that is drift rather than absence.
func subresourceRoutes() (map[string]serve.SubresourceRoute, error) {
	path := catalogEntryPath()
	if path == "" {
		return nil, fmt.Errorf("no CatalogEntry manifest found: set RAILGRID_CATALOGENTRY_FILE or bake %s under RAILGRID_KCP_DIR; without it the savedviews/run verb has no route and kuery must not serve", catalogEntryFileName)
	}
	routes, err := serve.SubresourcesFromCatalogEntryFile(path)
	if err != nil {
		return nil, err
	}
	log.Printf("kuery: serving %d declared coordinate(s) on the kcp custom-subresource path (from %s)", len(routes), path)
	return routes, nil
}
