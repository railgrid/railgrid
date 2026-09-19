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

	"github.com/railgrid/provider-kuery/install"
)

const (
	apiExportName = "kuery.providers.railgrid.ai"
)

// runInitCmd applies kuery's in-workspace objects using the workspace-admin
// kubeconfig the admin onboarded. Idempotent. Two separate sets, and the
// separation is the point:
//
//   - EXPORTED: the APIResourceSchemas under RAILGRID_KCP_DIR (SavedView, and
//     only SavedView), the generated APIExport that references them, its
//     endpoint slice and the bind grant. Anything in that directory becomes
//     bindable by every tenant — which is why codegen pins the export's
//     resource list to exactly the schemas the chart ships.
//   - PROVIDER-PRIVATE: the Engagement CRD, applied straight into the provider
//     workspace and deliberately absent from the export, so kuery's own
//     bookkeeping about which replica syncs which tenant's edge is not a
//     tenant-visible API. See the install package.
//
// kuery's APIExport (config/kcp/apiexport-kuery.providers.railgrid.ai.yaml,
// generated from manifest.yaml) carries NO permission claims at all.
//
// No first-party (*.railgrid.ai) claim, because such a claim must pin the
// serving APIExport's identityHash and an export pins exactly one identity per
// claimed resource — for every consuming workspace at once — which breaks the
// moment one org self-hosts the edges provider while others use the platform
// copy. What the engagement controller needs on an edge is declared as a
// COMPOSITION instead (manifest.yaml spec.dependencies[].composes) and reached
// through a hub-minted scoped identity acting inside each tenant workspace.
//
// And no built-in types either: those existed only to provision a
// ServiceAccount, a ClusterRole, a binding and a token Secret by hand. A
// provider does not mint identities, it asks the hub (engagement/identity.go,
// provider-sdk/identityclient).
func runInitCmd(ctx context.Context) error {
	config, err := loadProviderConfig()
	if err != nil {
		return fmt.Errorf("init needs a kubeconfig (set RAILGRID_PROVIDER_KUBECONFIG): %w", err)
	}
	// Empty means "the workspace this kubeconfig already points at": the SDK
	// resolves that workspace's canonical path and writes it onto the
	// APIExportEndpointSlice. Leaving it unset is what lets this one chart
	// bootstrap both the platform workspace and an org's self-hosted copy. Set
	// the env var only to reference an export in a different workspace.
	workspacePath := os.Getenv("KUERY_WORKSPACE_PATH")
	kcpDir := os.Getenv("RAILGRID_KCP_DIR")
	if kcpDir == "" {
		kcpDir = "/etc/railgrid/kcp"
	}
	// Per-installation APIExport identity hashes for first-party claim groups,
	// as "group=hash,group=hash". Empty for this provider: it claims nothing,
	// so there is no hash to stamp.
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

	// After the export, not before: a failure here must not leave a workspace
	// with private storage and no API.
	if err := install.EnsureEngagementCRD(ctx, config); err != nil {
		return err
	}

	log.Printf("kuery init: workspace bootstrapped (export=%s path=%s kcpDir=%s catalogEntry=%s, private Engagement CRD installed)",
		apiExportName, workspacePath, kcpDir, catalogEntryFile)
	return nil
}
