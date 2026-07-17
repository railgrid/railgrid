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
	"os"

	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/klog/v2"

	sdkinstall "github.com/faroshq/provider-sdk/install"
)

const (
	apiExportName        = "edges.providers.kedge.faros.sh"
	defaultWorkspacePath = "root:kedge:providers:edges"
)

// runInitCmd bootstraps the provider's APIExport into its workspace: it applies
// the KubernetesCluster + LinuxServer APIResourceSchemas from KEDGE_SCHEMAS_DIR,
// creates the edges.providers.kedge.faros.sh APIExport referencing them, the
// endpoint slice, and the bind grant. Tenants that bind this export get both
// edge kinds.
func runInitCmd(ctx context.Context) error {
	log := klog.Background().WithName("edges-init")

	config, err := loadInitConfig()
	if err != nil {
		return fmt.Errorf("init needs a kubeconfig (set KEDGE_PROVIDER_KUBECONFIG): %w", err)
	}
	workspacePath := os.Getenv("EDGES_WORKSPACE_PATH")
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
		// The APIExport MUST DECLARE the same permission claims the CatalogEntry
		// advertises (and tenants accept on Enable) — otherwise kcp marks the
		// APIBinding's claims "unexpected/invalid" and the core types never
		// surface in the APIExport virtual workspace, so the RBAC reconciler's
		// Owns(&Secret{}) informer fails ("no matches for kind Secret") and the
		// cluster never engages. These are the tenant-workspace objects the
		// token/RBAC/lifecycle reconcilers create per edge. Built-in types →
		// empty Group + no IdentityHash. Verbs MUST match the CatalogEntry.
		Claims: []sdkinstall.PermissionClaim{
			{Resource: "namespaces", Verbs: []string{"get", "list", "watch", "create", "update", "patch", "delete"}},
			{Resource: "serviceaccounts", Verbs: []string{"get", "list", "watch", "create", "update", "patch", "delete"}},
			{Resource: "secrets", Verbs: []string{"get", "list", "watch", "create", "update", "patch", "delete"}},
			{Group: "rbac.authorization.k8s.io", Resource: "clusterroles", Verbs: []string{"get", "list", "watch", "create", "update", "patch", "delete"}},
			{Group: "rbac.authorization.k8s.io", Resource: "clusterrolebindings", Verbs: []string{"get", "list", "watch", "create", "update", "patch", "delete"}},
		},
		CatalogEntryFile: catalogEntryFile,
	}); err != nil {
		return fmt.Errorf("provider workspace bootstrap: %w", err)
	}
	log.Info("edges init: workspace bootstrapped", "export", apiExportName, "path", workspacePath)
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
