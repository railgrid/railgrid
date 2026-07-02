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
	apiExportName        = "databricks.providers.kedge.faros.sh"
	defaultWorkspacePath = "root:kedge:providers:databricks"
)

func runInitCmd(ctx context.Context) error {
	config, err := loadInitConfig()
	if err != nil {
		return fmt.Errorf("init needs a kubeconfig (set KEDGE_PROVIDER_KUBECONFIG): %w", err)
	}
	workspacePath := envOr("DATABRICKS_WORKSPACE_PATH", defaultWorkspacePath)
	schemasDir := envOr("KEDGE_SCHEMAS_DIR", "/etc/kedge/schemas")
	catalogEntryFile := os.Getenv("KEDGE_CATALOGENTRY_FILE")

	if err := sdkinstall.Bootstrap(ctx, sdkinstall.Options{
		Config:        config,
		ExportName:    apiExportName,
		WorkspacePath: workspacePath,
		SchemasDir:    schemasDir,
		Claims: []sdkinstall.PermissionClaim{
			{Resource: "secrets", Verbs: []string{"get"}},
		},
		CatalogEntryFile: catalogEntryFile,
	}); err != nil {
		return fmt.Errorf("provider workspace bootstrap: %w", err)
	}
	log.Printf("databricks-provider init: workspace bootstrapped (export=%s path=%s schemas=%s catalogEntry=%s)", apiExportName, workspacePath, schemasDir, catalogEntryFile)
	return nil
}

func loadInitConfig() (*rest.Config, error) {
	if p := os.Getenv("KEDGE_PROVIDER_KUBECONFIG"); p != "" {
		return clientcmd.BuildConfigFromFlags("", p)
	}
	if p := os.Getenv("KUBECONFIG"); p != "" {
		return clientcmd.BuildConfigFromFlags("", p)
	}
	return rest.InClusterConfig()
}
