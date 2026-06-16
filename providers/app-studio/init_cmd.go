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
	apiExportName        = "ai.kedge.faros.sh"
	defaultWorkspacePath = "root:kedge:providers:app-studio"
)

// runInitCmd applies the App Studio provider's in-workspace objects
// (APIResourceSchemas, APIExport, APIExportEndpointSlice, bind grant) using the
// workspace-admin kubeconfig the admin onboarded. Idempotent.
func runInitCmd(ctx context.Context) error {
	config, err := loadProviderConfig()
	if err != nil {
		return fmt.Errorf("init needs a kubeconfig (set KEDGE_PROVIDER_KUBECONFIG): %w", err)
	}
	workspacePath := os.Getenv("APP_STUDIO_WORKSPACE_PATH")
	if workspacePath == "" {
		workspacePath = defaultWorkspacePath
	}
	schemasDir := os.Getenv("KEDGE_SCHEMAS_DIR")
	if schemasDir == "" {
		schemasDir = "/etc/kedge/schemas"
	}
	catalogEntryFile := os.Getenv("KEDGE_CATALOGENTRY_FILE")

	if err := sdkinstall.Bootstrap(ctx, sdkinstall.Options{
		Config:        config,
		ExportName:    apiExportName,
		WorkspacePath: workspacePath,
		SchemasDir:    schemasDir,
		// The provider stores per-project LLM credentials as Secrets (built-in
		// type → no identityHash).
		Claims: []sdkinstall.PermissionClaim{
			{Resource: "secrets", Verbs: []string{"get", "list", "watch", "create", "update", "delete"}},
		},
		CatalogEntryFile: catalogEntryFile,
	}); err != nil {
		return fmt.Errorf("provider workspace bootstrap: %w", err)
	}
	log.Printf("app-studio init: workspace bootstrapped (export=%s path=%s schemas=%s catalogEntry=%s)", apiExportName, workspacePath, schemasDir, catalogEntryFile)
	return nil
}
