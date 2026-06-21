/*
Copyright 2026 The Faros Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

// Package operator implements the CRD-driven kedge infrastructure operator:
// a controller-runtime manager that reconciles InfrastructureProvider CRs by
// bootstrapping the provider kcp workspace, lifecycling the kro Helm release,
// and owning the provider serve Deployment.
package operator

import (
	"context"
	"fmt"

	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"
	"k8s.io/klog/v2"

	sdkinstall "github.com/faroshq/provider-sdk/install"

	"github.com/faroshq/provider-infrastructure/install"
)

// BootstrapOptions parameterizes one bootstrap pass.
type BootstrapOptions struct {
	// WorkspacePath is the provider workspace (root:kedge:providers:infrastructure).
	WorkspacePath string
	// APIExportName is the provider's APIExport name.
	APIExportName string
	// CatalogEntryFile, when set, self-registers the CatalogEntry from this path.
	CatalogEntryFile string
	// SkipSeedTemplates leaves the catalog empty (GitOps-managed clusters).
	SkipSeedTemplates bool
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

	dynCl, err := dynamic.NewForConfig(providerCfg)
	if err != nil {
		return fmt.Errorf("dynamic client: %w", err)
	}

	if err := sdkinstall.ApplyAPIExport(ctx, dynCl, opts.APIExportName, nil, []sdkinstall.PermissionClaim{
		{Resource: "secrets", Verbs: []string{"get", "list", "watch"}},
	}); err != nil {
		return fmt.Errorf("materialize APIExport: %w", err)
	}
	if err := sdkinstall.ApplyBindGrant(ctx, dynCl, opts.APIExportName); err != nil {
		return fmt.Errorf("apply bind grant: %w", err)
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

	if opts.CatalogEntryFile != "" {
		if err := sdkinstall.ApplyCatalogEntry(ctx, dynCl, opts.CatalogEntryFile); err != nil {
			return fmt.Errorf("apply CatalogEntry: %w", err)
		}
	}

	if !opts.SkipSeedTemplates {
		if err := install.SeedTemplates(ctx, providerCfg); err != nil {
			// Non-fatal — the catalog can be managed out-of-band.
			log.Info("WARNING failed to seed Templates", "err", err.Error())
		}
	}

	return nil
}
