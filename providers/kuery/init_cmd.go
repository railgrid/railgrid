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
	apiExportName        = "kuery.providers.kedge.faros.sh"
	defaultWorkspacePath = "root:kedge:providers:kuery"
)

// runInitCmd applies kuery's in-workspace objects (APIResourceSchemas,
// APIExport, APIExportEndpointSlice, bind grant) using the workspace-admin
// kubeconfig the admin onboarded. Idempotent.
//
// kuery's edges permission claim is a FIRST-PARTY type (kedge.faros.sh), so its
// APIExport claim must carry the identityHash of the root APIExport that serves
// edges. The workspace-scoped kubeconfig can't read the parent workspace to
// resolve it, so the platform admin supplies it via KUERY_EDGES_IDENTITY_HASH
// (a Helm value copied from the /bonkers "Root identities" view). Without it the
// Enable flow's identity poll times out and kuery engages zero edges.
func runInitCmd(ctx context.Context) error {
	config, err := loadProviderConfig()
	if err != nil {
		return fmt.Errorf("init needs a kubeconfig (set KEDGE_PROVIDER_KUBECONFIG): %w", err)
	}
	workspacePath := os.Getenv("KUERY_WORKSPACE_PATH")
	if workspacePath == "" {
		workspacePath = defaultWorkspacePath
	}
	schemasDir := os.Getenv("KEDGE_SCHEMAS_DIR")
	if schemasDir == "" {
		schemasDir = "/etc/kedge/schemas"
	}

	edgesHash := os.Getenv("KUERY_EDGES_IDENTITY_HASH")
	if edgesHash == "" {
		log.Printf("WARNING KUERY_EDGES_IDENTITY_HASH is empty; the edges permission claim will have no identityHash and tenant Enable will not engage edges. Copy it from the /bonkers Root identities view into the chart value.")
	}
	catalogEntryFile := os.Getenv("KEDGE_CATALOGENTRY_FILE")

	if err := sdkinstall.Bootstrap(ctx, sdkinstall.Options{
		Config:        config,
		ExportName:    apiExportName,
		WorkspacePath: workspacePath,
		SchemasDir:    schemasDir,
		Claims: []sdkinstall.PermissionClaim{
			{
				Group:        "kedge.faros.sh",
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
