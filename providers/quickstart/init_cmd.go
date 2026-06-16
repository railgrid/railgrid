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

	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"

	sdkinstall "github.com/faroshq/provider-sdk/install"
)

const (
	apiExportName        = "quickstart.providers.kedge.faros.sh"
	defaultWorkspacePath = "root:kedge:providers:quickstart"
)

// runInitCmd applies the provider's in-workspace objects (APIResourceSchemas,
// APIExport, APIExportEndpointSlice, bind grant) using the workspace-admin
// kubeconfig the admin onboarded. Idempotent.
func runInitCmd(ctx context.Context) error {
	config, err := loadInitConfig()
	if err != nil {
		return fmt.Errorf("init needs a kubeconfig (set KEDGE_PROVIDER_KUBECONFIG): %w", err)
	}
	workspacePath := os.Getenv("QUICKSTART_WORKSPACE_PATH")
	if workspacePath == "" {
		workspacePath = defaultWorkspacePath
	}
	schemasDir := os.Getenv("KEDGE_SCHEMAS_DIR")
	if schemasDir == "" {
		schemasDir = "/etc/kedge/schemas"
	}

	if err := sdkinstall.Bootstrap(ctx, sdkinstall.Options{
		Config:        config,
		ExportName:    apiExportName,
		WorkspacePath: workspacePath,
		SchemasDir:    schemasDir,
		// Demo-only configmaps claim (built-in type → no identityHash).
		Claims: []sdkinstall.PermissionClaim{
			{Resource: "configmaps", Verbs: []string{"get", "list", "watch"}},
		},
	}); err != nil {
		return fmt.Errorf("provider workspace bootstrap: %w", err)
	}
	log.Printf("quickstart-provider init: workspace bootstrapped (export=%s path=%s schemas=%s)", apiExportName, workspacePath, schemasDir)
	return nil
}

// loadInitConfig resolves the workspace-admin kubeconfig for init.
func loadInitConfig() (*rest.Config, error) {
	if p := os.Getenv("KEDGE_PROVIDER_KUBECONFIG"); p != "" {
		return clientcmd.BuildConfigFromFlags("", p)
	}
	if p := os.Getenv("KUBECONFIG"); p != "" {
		return clientcmd.BuildConfigFromFlags("", p)
	}
	return rest.InClusterConfig()
}
