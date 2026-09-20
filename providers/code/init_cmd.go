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

	sdkinstall "github.com/railgrid/provider-sdk/install"

	"github.com/railgrid/provider-code/install"
)

// runInitCmd is the one-shot provider-workspace bootstrap. The admin onboarding
// API creates the provider workspace + ServiceAccount + kubeconfig; init applies
// everything that lives INSIDE the workspace using that kubeconfig:
// APIResourceSchemas, the APIExport, the APIExportEndpointSlice, and the bind
// RBAC grant. Idempotent; serve also ensures the slice at startup.
//
// The two declarative objects — the generated APIExport and the
// APIResourceSchemas it references — are read from RAILGRID_KCP_DIR (default
// /etc/railgrid/kcp), which is the chart's deploy/chart/files/ directory baked
// into the image. init applies them verbatim; nothing about the export is
// written in Go. Change a permission claim in manifest.yaml and re-run
// `make codegen-code-provider`.
func runInitCmd(ctx context.Context) error {
	config, err := loadControllerConfig()
	if err != nil {
		return fmt.Errorf("init needs a kubeconfig (set RAILGRID_PROVIDER_KUBECONFIG): %w", err)
	}
	// Empty means "the workspace this kubeconfig already points at": the SDK
	// resolves that workspace's canonical path and writes it onto the
	// APIExportEndpointSlice. Leaving it unset is what lets this one chart
	// bootstrap both the platform workspace and an org's self-hosted copy. Set
	// the env var only to reference an export in a different workspace.
	workspacePath := os.Getenv("CODE_WORKSPACE_PATH")
	kcpDir := os.Getenv("RAILGRID_KCP_DIR")
	if kcpDir == "" {
		kcpDir = "/etc/railgrid/kcp"
	}
	// Per-installation APIExport identity hashes for first-party claim groups,
	// as "group=hash,group=hash". Empty for this provider: it claims only
	// built-in Secrets, which need no hash.
	identityHashes, err := sdkinstall.ParseIdentityHashes(os.Getenv("RAILGRID_IDENTITY_HASHES"))
	if err != nil {
		return err
	}
	// CatalogEntry self-registration: the provider applies its own CatalogEntry
	// into its workspace (the Provider controller bound providers.railgrid.ai
	// here). Empty → skip.
	catalogEntryFile := os.Getenv("RAILGRID_CATALOGENTRY_FILE")

	if err := sdkinstall.Bootstrap(ctx, sdkinstall.Options{
		Config:           config,
		ExportName:       install.APIExportName,
		WorkspacePath:    workspacePath,
		KCPDir:           kcpDir,
		IdentityHashes:   identityHashes,
		CatalogEntryFile: catalogEntryFile,
	}); err != nil {
		return fmt.Errorf("provider workspace bootstrap: %w", err)
	}
	log.Printf("code-provider init: workspace bootstrapped (export=%s path=%s kcpDir=%s catalogEntry=%s)", install.APIExportName, workspacePath, kcpDir, catalogEntryFile)
	return nil
}
