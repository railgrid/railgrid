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

	"github.com/faroshq/provider-code/install"
)

// runInitCmd is the one-shot provider-workspace bootstrap. The admin onboarding
// API creates the provider workspace + ServiceAccount + kubeconfig; init applies
// everything that lives INSIDE the workspace using that kubeconfig:
// APIResourceSchemas, the APIExport, the APIExportEndpointSlice, and the bind
// RBAC grant. Idempotent; serve also ensures the slice at startup.
//
// Schemas are read from KEDGE_SCHEMAS_DIR (default /etc/kedge/schemas), which the
// Helm chart populates (and the dev Makefile points at deploy/chart/files/schemas).
func runInitCmd(ctx context.Context) error {
	config, err := loadControllerConfig()
	if err != nil {
		return fmt.Errorf("init needs a kubeconfig (set KEDGE_PROVIDER_KUBECONFIG): %w", err)
	}
	workspacePath := os.Getenv("CODE_WORKSPACE_PATH")
	if workspacePath == "" {
		workspacePath = defaultWorkspacePath
	}
	schemasDir := os.Getenv("KEDGE_SCHEMAS_DIR")
	if schemasDir == "" {
		schemasDir = "/etc/kedge/schemas"
	}
	// CatalogEntry self-registration: the provider applies its own CatalogEntry
	// into its workspace (the Provider controller bound providers.kedge.faros.sh
	// here). Empty → skip.
	catalogEntryFile := os.Getenv("KEDGE_CATALOGENTRY_FILE")

	if err := sdkinstall.Bootstrap(ctx, sdkinstall.Options{
		Config:           config,
		ExportName:       install.APIExportName,
		WorkspacePath:    workspacePath,
		SchemasDir:       schemasDir,
		Claims:           codeClaims(),
		CatalogEntryFile: catalogEntryFile,
	}); err != nil {
		return fmt.Errorf("provider workspace bootstrap: %w", err)
	}
	log.Printf("code-provider init: workspace bootstrapped (export=%s path=%s schemas=%s catalogEntry=%s)", install.APIExportName, workspacePath, schemasDir, catalogEntryFile)
	return nil
}

// codeClaims declares the code provider's APIExport permission claims. The
// secrets claim is a built-in k8s type (empty group), so it needs no
// identityHash. The controllers read each Connection's PAT Secret and write the
// generated DeployKey private-key Secret, hence the write verbs.
func codeClaims() []sdkinstall.PermissionClaim {
	return []sdkinstall.PermissionClaim{
		{
			Resource: "secrets",
			Verbs:    []string{"get", "list", "watch", "create", "update", "patch", "delete"},
		},
	}
}
