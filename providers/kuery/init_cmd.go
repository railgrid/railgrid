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
// generated from manifest.yaml) carries no HAND-WRITTEN permission claim: every
// claim on it is derived from manifest.yaml spec.requires, which is the one
// place kuery declares what it needs that it does not own.
//
// And none of those claims PINS an identityHash. A pinned first-party
// (*.railgrid.ai) claim fixes exactly one serving identity per claimed resource
// — for every consuming workspace at once — which breaks the moment one org
// self-hosts the edges provider while others use the platform copy. An unpinned
// claim is resolved per consumer workspace against whatever export that
// workspace actually bound, which is what lets the engagement controller reach
// an edge through kuery's own export virtual workspace.
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
	// The same lookup serve uses to derive its custom-subresource routes
	// (catalogentry.go), so init and serve can never read a different
	// declaration. Empty → skip.
	catalogEntryFile := catalogEntryPath()
	// Where kcp reverse-proxies a custom subresource request to. Empty means
	// "spec.serving.backend.url of the CatalogEntry above", which is right whenever the
	// chart runs init; a harness that registers the CatalogEntry itself (the
	// Makefile's install-provider-* target, the provider e2e) has no file to
	// read it from and sets this instead.
	dataPlaneURL := strings.TrimSpace(os.Getenv("RAILGRID_DATAPLANE_URL"))

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

	// After the export, not before: a failure here must not leave a workspace
	// with private storage and no API.
	if err := install.EnsureEngagementCRD(ctx, config); err != nil {
		return err
	}

	log.Printf("kuery init: workspace bootstrapped (export=%s path=%s kcpDir=%s catalogEntry=%s, private Engagement CRD installed)",
		apiExportName, workspacePath, kcpDir, catalogEntryFile)
	return nil
}
