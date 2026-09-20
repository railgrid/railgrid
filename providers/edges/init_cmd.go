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
	"os"

	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/klog/v2"

	sdkinstall "github.com/railgrid/provider-sdk/install"
)

const (
	apiExportName = "edges.providers.railgrid.ai"
)

// runInitCmd bootstraps the provider's APIExport into its workspace: it applies
// the APIResourceSchemas from RAILGRID_KCP_DIR, then the generated
// edges.providers.railgrid.ai APIExport that references them, the endpoint
// slice, and the bind grant. Tenants that bind this export get every edge kind.
//
// The APIExport MUST DECLARE the same permission claims the CatalogEntry
// advertises (and tenants accept on Enable) — otherwise kcp marks the
// APIBinding's claims "unexpected/invalid", the core types never surface in the
// APIExport virtual workspace, and the RBAC reconciler's Owns(&Secret{})
// informer fails ("no matches for kind Secret") so the cluster never engages.
// That is now structural rather than a rule to remember: both come from
// manifest.yaml, codegen writes the export, and init applies it as-is.
func runInitCmd(ctx context.Context) error {
	log := klog.Background().WithName("edges-init")

	config, err := loadInitConfig()
	if err != nil {
		return fmt.Errorf("init needs a kubeconfig (set RAILGRID_PROVIDER_KUBECONFIG): %w", err)
	}
	// Empty means "the workspace this kubeconfig already points at": the SDK
	// resolves that workspace's canonical path and writes it onto the
	// APIExportEndpointSlice. Leaving it unset is what lets this one chart
	// bootstrap both the platform workspace and an org's self-hosted copy. Set
	// the env var only to reference an export in a different workspace.
	workspacePath := os.Getenv("EDGES_WORKSPACE_PATH")
	kcpDir := os.Getenv("RAILGRID_KCP_DIR")
	if kcpDir == "" {
		kcpDir = "/etc/railgrid/kcp"
	}
	// Per-installation APIExport identity hashes for first-party claim groups,
	// as "group=hash,group=hash". Empty for this provider: it claims only
	// built-in types, which need no hash.
	identityHashes, err := sdkinstall.ParseIdentityHashes(os.Getenv("RAILGRID_IDENTITY_HASHES"))
	if err != nil {
		return err
	}
	catalogEntryFile := os.Getenv("RAILGRID_CATALOGENTRY_FILE")

	if err := sdkinstall.Bootstrap(ctx, sdkinstall.Options{
		Config:           config,
		ExportName:       apiExportName,
		WorkspacePath:    workspacePath,
		KCPDir:           kcpDir,
		IdentityHashes:   identityHashes,
		CatalogEntryFile: catalogEntryFile,
	}); err != nil {
		return fmt.Errorf("provider workspace bootstrap: %w", err)
	}
	log.Info("edges init: workspace bootstrapped", "export", apiExportName, "path", workspacePath)
	return nil
}

func loadInitConfig() (*rest.Config, error) {
	if p := os.Getenv("RAILGRID_PROVIDER_KUBECONFIG"); p != "" {
		return clientcmd.BuildConfigFromFlags("", p)
	}
	if p := os.Getenv("KUBECONFIG"); p != "" {
		return clientcmd.BuildConfigFromFlags("", p)
	}
	return rest.InClusterConfig()
}
