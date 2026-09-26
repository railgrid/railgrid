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
	"strings"

	sdkinstall "github.com/railgrid/provider-sdk/install"
)

const (
	apiExportName = "ai.railgrid.ai"
	// providerName is what this provider's CatalogEntry registers it as, and
	// therefore how it names itself to the hub — in the heartbeat, and in the
	// owner tuple of every identity it asks the hub to mint.
	providerName = "app-studio"
)

// No claim carries an identityHash: the first-party claims derived from
// spec.requires are identity-agnostic, resolved by kcp per consuming workspace
// against whichever copy of infrastructure or code that workspace bound.
// kcp now resolves an unpinned claim per consuming workspace, against a
// cluster-scoped PermissionClaimPolicy that pairs this export's group with the
// claimed group, so each workspace is served the copy it enabled.
//
// What may be claimed is still declared, not assumed: manifest.yaml
// spec.requires, accepted by the tenant at Enable, is what both the claims and
// the identity rules are generated from. The per-project and
// per-Studio identities MINTED BY THE HUB (controller/project/identity.go,
// controller/studio/identity.go) remain for what a claim cannot grant — a
// bearer another provider's data plane accepts — and for the per-workspace
// dependency watch.

// runInitCmd applies the App Studio provider's in-workspace objects
// (APIResourceSchemas, APIExport, APIExportEndpointSlice, bind grant) using the
// workspace-admin kubeconfig the admin onboarded. Idempotent.
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
	workspacePath := os.Getenv("APP_STUDIO_WORKSPACE_PATH")
	kcpDir := os.Getenv("RAILGRID_KCP_DIR")
	if kcpDir == "" {
		kcpDir = "/etc/railgrid/kcp"
	}
	// The lookup is shared with serve (catalogentry.go), which derives its
	// custom-subresource routes from the same document.
	catalogEntryFile := catalogEntryPath()
	// Where kcp reverse-proxies a custom subresource request to. Empty means
	// "spec.serving.backend.url of the CatalogEntry above", which is right whenever the
	// chart runs init; a harness that registers the CatalogEntry itself (the
	// Makefile's install-provider-* target, the provider e2e) has no file to
	// read it from and sets this instead.
	dataPlaneURL := strings.TrimSpace(os.Getenv("RAILGRID_DATAPLANE_URL"))

	// The APIExport this applies claims Secrets — the credential material this
	// provider writes itself — plus the three dependency kinds the reconcilers
	// converge: instances, repositories and repositorycommits. All four are
	// declared in manifest.yaml spec.requires — one list, keyed by API group —
	// and stamped onto the APIExport by codegen; none carries an identityHash,
	// for the reason above.
	if err := sdkinstall.Bootstrap(ctx, sdkinstall.Options{
		Config:           config,
		ExportName:       apiExportName,
		WorkspacePath:    workspacePath,
		KCPDir:           kcpDir,
		CatalogEntryFile: catalogEntryFile,
		DataPlaneURL:     dataPlaneURL,
	}); err != nil {
		return fmt.Errorf("provider workspace bootstrap: %w", err)
	}
	log.Printf("app-studio init: workspace bootstrapped (export=%s path=%s kcpDir=%s catalogEntry=%s)", apiExportName, workspacePath, kcpDir, catalogEntryFile)
	return nil
}
