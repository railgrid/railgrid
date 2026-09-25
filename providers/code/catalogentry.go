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

	"github.com/railgrid/provider-code/actions"
)

// The CatalogEntry manifest is this provider's single declaration of what it
// serves: `init` applies it into the provider workspace, apiexportgen turns the
// same document into the APIExport's "<resource>/<verb>" entries, and serve
// derives from it the routes it answers on the path a kcp shard forwards for a
// custom subresource — the only path a verb is reached on. Both subcommands
// run from the same image with the same env, so they must resolve it the same
// way — hence one lookup, here.

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
// actually there: init treats a non-empty path as "apply this", and the
// difference between "no manifest mounted" and "a manifest that does not
// parse" matters to the error serve reports.
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

// actionsRouteVerbs are this provider's two coordinates that are DECLARED as
// data-plane verbs and SERVED by the actions handler.
//
// stage-snapshot and stage-commit-bundle carry bodies (a 25 MiB git bundle, a
// 48 MiB source tree) that CatalogEntry.spec.actions cannot describe, because
// limits.maxInputBytes is capped at 1 MiB — so they are declared under
// spec.dataPlane.verbs to keep their coordinate grantable, and documented as
// the catalogue's one exception (docs/provider-actions.md §"Uncatalogued
// large-upload verbs"). What actually serves them is actions/server.go,
// exactly like a catalogued action at the same contract version — this
// provider registers no separate data-plane handler at all.
//
// The derived table's route for these two is therefore corrected to the
// actions handler. The SET of coordinates still comes only from the manifest,
// and a coordinate that has disappeared from it is an error rather than a
// silent no-op.
var actionsRouteVerbs = map[string]string{
	"repositories/" + actions.StageSnapshot:     actions.ContractVersion,
	"repositories/" + actions.StageCommitBundle: actions.ContractVersion,
}

// subresourceRoutes derives serve.Options.Subresources from the CatalogEntry
// manifest, so the coordinates this provider answers on the shard-forwarded
// path can never drift from the ones it declares.
//
// There is no fallback: a verb is reached only as a kcp custom subresource, so
// a provider with no manifest has no data plane at all, and starting anyway
// would silently serve a catalogue it cannot honour. A missing, unreadable or
// invalid manifest is a startup error.
func subresourceRoutes() (map[string]serve.SubresourceRoute, error) {
	path := catalogEntryPath()
	if path == "" {
		return nil, fmt.Errorf("code: no CatalogEntry manifest found; set RAILGRID_CATALOGENTRY_FILE or bake %s under RAILGRID_KCP_DIR — a verb is reached only as a kcp custom subresource, so without the declaration this provider has no data plane", catalogEntryFileName)
	}
	routes, err := serve.SubresourcesFromCatalogEntryFile(path)
	if err != nil {
		return nil, err
	}
	for coordinate, version := range actionsRouteVerbs {
		if _, declared := routes[coordinate]; !declared {
			return nil, fmt.Errorf("code: %s is served by the actions handler but %s no longer declares it; the coordinate must stay declared (as a data-plane verb) or stop being served", coordinate, path)
		}
		routes[coordinate] = serve.SubresourceRoute{Action: true, Version: version}
	}
	log.Printf("code: serving %d declared coordinate(s) on the kcp custom-subresource path (from %s)", len(routes), path)
	return routes, nil
}
