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
	apiExportName = "agents.railgrid.ai"
)

// runInitCmd applies the provider's in-workspace objects (APIResourceSchemas,
// APIExport, APIExportEndpointSlice, bind grant, and optionally the
// CatalogEntry) using the workspace-admin kubeconfig. Idempotent.
//
// The schemas and the APIExport are read from RAILGRID_KCP_DIR (default
// /etc/railgrid/kcp, the chart's files/ directory baked into the image) and
// applied verbatim. The provider's permission claims — Secrets for model
// credentials, the ServiceAccount/RBAC trio behind per-agent background-run
// identity, and the TokenReview/SubjectAccessReview pair behind
// service-to-service invocation — are declared once in manifest.yaml and
// codegen stamps them onto that file. Built-in groups all: no identityHash.
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
	workspacePath := os.Getenv("AGENTS_WORKSPACE_PATH")
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
	log.Printf("agents-provider init: workspace bootstrapped (export=%s path=%s kcpDir=%s catalogEntry=%s)", apiExportName, workspacePath, kcpDir, catalogEntryFile)
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
