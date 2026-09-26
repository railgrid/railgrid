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
// failing. serve is stricter — see subresourceRoutes.
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

// errNoCatalogEntry is returned by subresourceRoutes when no manifest can be
// found. It is a startup failure, not a degraded mode.
var errNoCatalogEntry = errors.New("no CatalogEntry manifest found; set RAILGRID_CATALOGENTRY_FILE or bake " + catalogEntryFileName + " under RAILGRID_KCP_DIR")

// subresourceRoutes derives serve.Options.Subresources from the CatalogEntry
// manifest, so the coordinates this provider answers on the shard-forwarded
// path can never drift from the ones it declares.
//
// A verb is reached ONLY as a kcp custom subresource, so a provider with no
// manifest has no data plane at all — not a reduced one. That is a startup
// error rather than a log line: a serve.New with a DataPlane handler and no
// Subresources refuses to build, and this reports the cause (the missing
// manifest) instead of letting serve report the symptom. An unreadable or
// invalid manifest is equally an error, because that is drift.
func subresourceRoutes() (map[string]serve.SubresourceRoute, error) {
	path := catalogEntryPath()
	if path == "" {
		return nil, errNoCatalogEntry
	}
	routes, err := serve.SubresourcesFromCatalogEntryFile(path)
	if err != nil {
		return nil, err
	}
	if len(routes) == 0 {
		return nil, fmt.Errorf("%s declares no data-plane verbs; every project, session and studio verb would be unreachable", path)
	}
	log.Printf("app-studio: serving %d declared coordinate(s) as kcp custom subresources (from %s)", len(routes), path)
	return routes, nil
}
