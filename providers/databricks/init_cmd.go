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

	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

const (
	apiExportName        = "databricks.providers.faros.sh"
)

// permissionClaims is the single init-side declaration of the provider's
// authority. Keep it in lockstep with manifest.yaml and the Helm CatalogEntry:
// existing tenant APIBindings do not gain newly advertised claims until they
// are explicitly re-enabled or migrated.
var permissionClaims = []sdkinstall.PermissionClaim{
	{Resource: "secrets", Verbs: []string{"get"}},
}

func runInitCmd(ctx context.Context) error {
	config, err := loadInitConfig()
	if err != nil {
		return fmt.Errorf("init needs a kubeconfig (set FAROS_PROVIDER_KUBECONFIG): %w", err)
	}
	// Empty means "the workspace this kubeconfig already points at": kcp resolves
	// an unset export path to the slice's own logical cluster, so one chart works
	// for both the platform workspace and an org's self-hosted copy.
	workspacePath := envOr("DATABRICKS_WORKSPACE_PATH", "")
	schemasDir := envOr("FAROS_SCHEMAS_DIR", "/etc/faros/schemas")
	catalogEntryFile := os.Getenv("FAROS_CATALOGENTRY_FILE")

	if err := sdkinstall.Bootstrap(ctx, sdkinstall.Options{
		Config:           config,
		ExportName:       apiExportName,
		WorkspacePath:    workspacePath,
		SchemasDir:       schemasDir,
		Claims:           permissionClaims,
		CatalogEntryFile: catalogEntryFile,
	}); err != nil {
		return fmt.Errorf("provider workspace bootstrap: %w", err)
	}
	log.Printf("databricks-provider init: workspace bootstrapped (export=%s path=%s schemas=%s catalogEntry=%s)", apiExportName, workspacePath, schemasDir, catalogEntryFile)
	return nil
}

func loadInitConfig() (*rest.Config, error) {
	if p := os.Getenv("FAROS_PROVIDER_KUBECONFIG"); p != "" {
		return clientcmd.BuildConfigFromFlags("", p)
	}
	if p := os.Getenv("KUBECONFIG"); p != "" {
		return clientcmd.BuildConfigFromFlags("", p)
	}
	return rest.InClusterConfig()
}
