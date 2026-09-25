/*
Copyright 2026 The Railgrid Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package main

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"

	"github.com/railgrid/provider-sdk/serve"

	"github.com/railgrid/provider-infrastructure/install"
)

// The CatalogEntry manifest is this provider's single declaration of what it
// serves: `init` (and the operator's bootstrap) applies it into the provider
// workspace, apiexportgen turns the same document into the APIExport's
// "<resource>/<verb>" entries, and serve derives from it the routes it answers
// on the path a kcp shard forwards for a custom subresource. All of them run
// from the same image with the same env, so they must resolve it the same way
// — hence one lookup, here.

// catalogEntryFileName is the name the chart's ConfigMap gives the manifest,
// and the name to look for when only the kcp object directory is pointed at.
const catalogEntryFileName = "catalogentry.yaml"

// catalogEntryPath returns the CatalogEntry manifest this image ships, or ""
// when none is mounted.
//
// RAILGRID_CATALOGENTRY_FILE is the explicit path (the chart renders the entry
// into a ConfigMap and mounts it at /etc/railgrid/catalogentry). Failing that,
// install.KCPDir() — RAILGRID_KCP_DIR, or the image's baked /etc/railgrid/kcp
// — is searched for the same file name, for an image that bakes it beside the
// other kcp objects.
//
// A path derived from the kcp directory is returned only when the file is
// actually there: bootstrap treats a non-empty path as "apply this". serve,
// by contrast, cannot run without it (subresourceRoutes).
func catalogEntryPath() string {
	if p := strings.TrimSpace(os.Getenv("RAILGRID_CATALOGENTRY_FILE")); p != "" {
		return p
	}
	dir := strings.TrimSpace(install.KCPDir())
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
// A verb is reached exactly one way — as a kcp custom subresource — so a
// serve process with no manifest has no data plane at all, and serve.New
// refuses a DataPlane handler with nothing to reach it through. Both absence
// and an unreadable or invalid manifest are therefore startup errors: set
// RAILGRID_CATALOGENTRY_FILE, or bake catalogentry.yaml under RAILGRID_KCP_DIR
// beside the other kcp objects.
func subresourceRoutes() (map[string]serve.SubresourceRoute, error) {
	path := catalogEntryPath()
	if path == "" {
		return nil, fmt.Errorf("no CatalogEntry manifest found: set RAILGRID_CATALOGENTRY_FILE or bake %s under RAILGRID_KCP_DIR; a verb is reached only as a kcp custom subresource, so without the declaration this provider has no data plane", catalogEntryFileName)
	}
	routes, err := serve.SubresourcesFromCatalogEntryFile(path)
	if err != nil {
		return nil, err
	}
	log.Printf("infrastructure: serving %d declared coordinate(s) on the kcp custom-subresource path (from %s)", len(routes), path)
	return routes, nil
}
