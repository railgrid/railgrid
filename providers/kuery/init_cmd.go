// Copyright 2026 The Faros Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package main

import (
	"context"
	"fmt"
	"log"
	"os"

	sdkinstall "github.com/faroshq/provider-sdk/install"
)

const (
	apiExportName        = "kuery.providers.faros.sh"
)

// runInitCmd applies kuery's in-workspace objects (APIResourceSchemas,
// APIExport, APIExportEndpointSlice, bind grant) using the workspace-admin
// kubeconfig the admin onboarded. Idempotent.
//
// kuery's edges permission claim is a FIRST-PARTY type (faros.sh), so its
// APIExport claim must carry the identityHash of the root APIExport that serves
// edges. The workspace-scoped kubeconfig can't read the parent workspace to
// resolve it, so the platform admin supplies it via KUERY_EDGES_IDENTITY_HASH
// (a Helm value copied from the /bonkers "Root identities" view). Without it the
// Enable flow's identity poll times out and kuery engages zero edges.
func runInitCmd(ctx context.Context) error {
	config, err := loadProviderConfig()
	if err != nil {
		return fmt.Errorf("init needs a kubeconfig (set FAROS_PROVIDER_KUBECONFIG): %w", err)
	}
	// Empty means "the workspace this kubeconfig already points at": kcp
	// resolves an unset APIExportEndpointSlice export path to the slice's own
	// logical cluster. Leaving it unset is what lets this one chart bootstrap
	// both the platform workspace and an org's self-hosted copy. Set the env
	// var only to reference an export in a different workspace.
	workspacePath := os.Getenv("KUERY_WORKSPACE_PATH")
	schemasDir := os.Getenv("FAROS_SCHEMAS_DIR")
	if schemasDir == "" {
		schemasDir = "/etc/faros/schemas"
	}

	edgesHash := os.Getenv("KUERY_EDGES_IDENTITY_HASH")
	if edgesHash == "" {
		log.Printf("WARNING KUERY_EDGES_IDENTITY_HASH is empty; the edges permission claim will have no identityHash and tenant Enable will not engage edges. Copy it from the /bonkers Root identities view into the chart value.")
	}
	catalogEntryFile := os.Getenv("FAROS_CATALOGENTRY_FILE")

	if err := sdkinstall.Bootstrap(ctx, sdkinstall.Options{
		Config:        config,
		ExportName:    apiExportName,
		WorkspacePath: workspacePath,
		SchemasDir:    schemasDir,
		Claims: []sdkinstall.PermissionClaim{
			{
				Group:        "faros.sh",
				Resource:     "edges",
				Verbs:        []string{"get", "list", "watch"},
				IdentityHash: edgesHash,
			},
		},
		CatalogEntryFile: catalogEntryFile,
	}); err != nil {
		return fmt.Errorf("provider workspace bootstrap: %w", err)
	}
	log.Printf("kuery init: workspace bootstrapped (export=%s path=%s schemas=%s edgesHash=%t catalogEntry=%s)", apiExportName, workspacePath, schemasDir, edgesHash != "", catalogEntryFile)
	return nil
}
