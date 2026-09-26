// Copyright 2026 The Railgrid Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"

	"github.com/go-logr/logr"

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

// errNoCatalogEntry is returned when no manifest is mounted. A verb is reached
// only as a kcp custom subresource, and the manifest is the only source of
// which coordinates exist, so a provider without one has no data plane — and
// serve.New refuses a DataPlane handler with no Subresources to reach it
// through. Failing here names the cause rather than letting serve.New fail
// one step later on a symptom.
var errNoCatalogEntry = errors.New("no CatalogEntry manifest found; set RAILGRID_CATALOGENTRY_FILE or put " + catalogEntryFileName + " in RAILGRID_KCP_DIR (a verb exists only as a declared custom subresource, so without the declaration there is no data plane to serve)")

// subresourceRoutes derives serve.Options.Subresources from the CatalogEntry
// manifest, so the coordinates this provider answers can never drift from the
// ones it declares. No manifest, an unreadable one, or a declared verb kcp
// would refuse is an error at startup rather than at a tenant's first call.
func subresourceRoutes(log logr.Logger) (map[string]serve.SubresourceRoute, error) {
	path := catalogEntryPath()
	if path == "" {
		return nil, errNoCatalogEntry
	}
	routes, err := serve.SubresourcesFromCatalogEntryFile(path)
	if err != nil {
		return nil, err
	}
	log.Info("serving the declared coordinates as kcp custom subresources", "count", len(routes), "manifest", path)
	return routes, nil
}
