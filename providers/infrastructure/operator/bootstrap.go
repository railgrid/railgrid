/*
Copyright 2026 The Railgrid Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

// Package operator implements the CRD-driven railgrid infrastructure operator:
// a controller-runtime manager that reconciles InfrastructureProvider CRs by
// bootstrapping the provider kcp workspace, lifecycling the kro Helm release,
// and owning the provider serve Deployment.
package operator

import (
	"context"
	"fmt"

	"k8s.io/client-go/rest"
	"k8s.io/klog/v2"

	sdkinstall "github.com/railgrid/provider-sdk/install"

	"github.com/railgrid/provider-infrastructure/install"
)

// BootstrapOptions parameterizes one bootstrap pass.
type BootstrapOptions struct {
	// WorkspacePath is the provider workspace (root:railgrid:providers:infrastructure).
	WorkspacePath string
	// APIExportName is the provider's APIExport name.
	APIExportName string
	// KCPDir holds the generated APIExport (apiexport.yaml). Empty resolves to
	// RAILGRID_KCP_DIR, then to the image's baked copy at
	// install.DefaultKCPDir — which is what every in-cluster caller wants.
	KCPDir string
	// CatalogEntryFile, when set, self-registers the CatalogEntry from this path.
	CatalogEntryFile string
	// DataPlaneURL is where kcp reverse-proxies an instances/<verb> request to.
	// Empty means spec.backend.url of CatalogEntryFile, which is right whenever
	// the chart runs the operator.
	DataPlaneURL string
	// SkipSeedTemplates leaves the catalog empty (GitOps-managed clusters).
	SkipSeedTemplates bool
	// CodingSandboxEnabled opts the platform-owned universal coding sandbox
	// into the embedded catalog seed. It is false by default.
	CodingSandboxEnabled bool
}

// Bootstrap runs one idempotent pass of the provider-workspace bootstrap using
// the provider (kcp) config: CRDs, APIExport shell + bind grant, the Templates
// CachedResource + its EndpointSlice, the APIExportEndpointSlice kro watches,
// the APIExport schema registration, and (optionally) catalog seeding. It does
// NOT seed kro — that is a separate step the caller owns so it can order the kro
// namespace/release around it.
func Bootstrap(ctx context.Context, providerCfg *rest.Config, opts BootstrapOptions) error {
	log := klog.FromContext(ctx)

	if err := install.CRDs(ctx, providerCfg); err != nil {
		return fmt.Errorf("install CRDs: %w", err)
	}

	kcpDir := opts.KCPDir
	if kcpDir == "" {
		kcpDir = install.KCPDir()
	}
	// The same bootstrap every provider runs: the shipped schemas (instances,
	// templates and one per instances/<verb> subresource), the
	// DataPlaneEndpointSlice, the generated APIExport, its endpoint slice, the
	// bind grant and the CatalogEntry. Templates land on CRD storage first and
	// are re-pointed at virtual storage below, once the identityHash is known.
	if err := sdkinstall.Bootstrap(ctx, sdkinstall.Options{
		Config:           providerCfg,
		ExportName:       opts.APIExportName,
		WorkspacePath:    opts.WorkspacePath,
		KCPDir:           kcpDir,
		CatalogEntryFile: opts.CatalogEntryFile,
		DataPlaneURL:     opts.DataPlaneURL,
	}); err != nil {
		return fmt.Errorf("provider workspace bootstrap: %w", err)
	}

	if err := install.PlatformCachedResources(ctx, providerCfg); err != nil {
		return fmt.Errorf("install CachedResource: %w", err)
	}
	if err := install.PlatformCachedResourceEndpointSlices(ctx, providerCfg); err != nil {
		return fmt.Errorf("install CachedResourceEndpointSlice: %w", err)
	}
	if err := install.PlatformAPIExportEndpointSlice(ctx, providerCfg, opts.WorkspacePath); err != nil {
		return fmt.Errorf("install APIExportEndpointSlice: %w", err)
	}

	// Templates MUST be served via the CachedResource virtual storage so they
	// project into tenant workspaces. Never fall back to CRD storage (which is an
	// empty per-tenant CRD — tenants would see no templates). Fail instead and
	// let the reconcile retry until the CachedResource identityHash is ready.
	hash, err := install.WaitForCachedResourceIdentity(ctx, providerCfg)
	if err != nil {
		return fmt.Errorf("CachedResource identityHash not ready (templates require virtual storage): %w", err)
	}
	if hash == "" {
		return fmt.Errorf("CachedResource identityHash empty (templates require virtual storage)")
	}
	if err := install.PlatformSchemaInAPIExport(ctx, providerCfg, hash); err != nil {
		return fmt.Errorf("register APIExport schemas: %w", err)
	}

	if !opts.SkipSeedTemplates {
		if err := install.SeedTemplatesWithOptions(ctx, providerCfg, install.SeedTemplatesOptions{
			CodingSandboxEnabled: opts.CodingSandboxEnabled,
		}); err != nil {
			// Non-fatal — the catalog can be managed out-of-band.
			log.Info("WARNING failed to seed Templates", "err", err.Error())
		}
	}

	return nil
}
