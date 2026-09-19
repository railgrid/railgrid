// Copyright 2026 The Railgrid Authors.
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

	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"

	sdkinstall "github.com/railgrid/provider-sdk/install"
)

const (
	apiExportName = "quickstart.providers.railgrid.ai"
)

// runInitCmd applies the provider's in-workspace objects (APIResourceSchemas,
// APIExport, APIExportEndpointSlice, bind grant) using the workspace-admin
// kubeconfig the admin onboarded. Idempotent.
func runInitCmd(ctx context.Context) error {
	config, err := loadInitConfig()
	if err != nil {
		return fmt.Errorf("init needs a kubeconfig (set RAILGRID_PROVIDER_KUBECONFIG): %w", err)
	}
	// Empty means "the workspace this kubeconfig already points at": the SDK
	// resolves that workspace's canonical path and writes it onto the
	// APIExportEndpointSlice. Leaving it unset is what lets this one chart
	// bootstrap both the platform workspace and an org's self-hosted copy. Set
	// the env var only to reference an export in a different workspace.
	workspacePath := os.Getenv("QUICKSTART_WORKSPACE_PATH")
	schemasDir := os.Getenv("RAILGRID_SCHEMAS_DIR")
	if schemasDir == "" {
		schemasDir = "/etc/railgrid/schemas"
	}
	// CatalogEntry self-registration: the provider applies its own CatalogEntry
	// into its workspace (the hub watches it there). Empty → skip.
	catalogEntryFile := os.Getenv("RAILGRID_CATALOGENTRY_FILE")

	// Bootstrap applies, idempotently and in order: the APIResourceSchemas in
	// SchemasDir, the APIExport that serves them, the APIExportEndpointSlice
	// the controller manager watches to discover tenant workspaces, and the
	// bind grant that lets tenants create an APIBinding. Without the slice the
	// multicluster manager has nothing to watch and no Greeting anywhere is
	// ever reconciled, so this one call is what makes the controller real.
	//
	// Claims is empty on purpose. A permission claim is access the provider
	// asks every tenant to grant it in their own workspace; this one reconciles
	// only the Greetings its own APIExport serves, so it needs none. Claims
	// declared here MUST match manifest.yaml and
	// deploy/chart/templates/catalogentry.yaml exactly — all three are the same
	// promise written down three times, and hack/verify-provider-contract.mjs
	// fails the build when they drift.
	if err := sdkinstall.Bootstrap(ctx, sdkinstall.Options{
		Config:           config,
		ExportName:       apiExportName,
		WorkspacePath:    workspacePath,
		SchemasDir:       schemasDir,
		CatalogEntryFile: catalogEntryFile,
	}); err != nil {
		return fmt.Errorf("provider workspace bootstrap: %w", err)
	}
	log.Printf("quickstart-provider init: workspace bootstrapped (export=%s path=%s schemas=%s catalogEntry=%s)", apiExportName, workspacePath, schemasDir, catalogEntryFile)
	return nil
}

// loadInitConfig resolves the workspace-admin kubeconfig for init.
func loadInitConfig() (*rest.Config, error) {
	if p := os.Getenv("RAILGRID_PROVIDER_KUBECONFIG"); p != "" {
		return clientcmd.BuildConfigFromFlags("", p)
	}
	if p := os.Getenv("KUBECONFIG"); p != "" {
		return clientcmd.BuildConfigFromFlags("", p)
	}
	return rest.InClusterConfig()
}
