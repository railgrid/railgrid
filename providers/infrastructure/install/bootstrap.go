/*
Copyright 2026 The Railgrid Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package install

import (
	"context"
	"fmt"

	sdkinstall "github.com/railgrid/provider-sdk/install"
)

// BootstrapWorkspace is the shared init/operator installation sequence. Publish
// Templates with virtual storage on the first export write, including upgrades.
// The cache slice may precede its export: kcp's slice reconciler marks its
// APIExportValid condition false until the export appears, then reconciles it.
func BootstrapWorkspace(ctx context.Context, opts sdkinstall.Options) error {
	if err := CRDs(ctx, opts.Config); err != nil {
		return fmt.Errorf("install CRDs: %w", err)
	}
	if err := PlatformCachedResources(ctx, opts.Config); err != nil {
		return fmt.Errorf("install CachedResource: %w", err)
	}
	if err := PlatformCachedResourceEndpointSlices(ctx, opts.Config); err != nil {
		return fmt.Errorf("install CachedResourceEndpointSlice: %w", err)
	}
	opts.VirtualStorage = TemplateVirtualStorage()
	if err := sdkinstall.Bootstrap(ctx, opts); err != nil {
		return fmt.Errorf("provider workspace bootstrap: %w", err)
	}
	if err := PlatformAPIExportEndpointSlice(ctx, opts.Config, opts.WorkspacePath); err != nil {
		return fmt.Errorf("install APIExportEndpointSlice: %w", err)
	}
	// Readiness is checked after publication, so slow cache identity generation
	// cannot block schemas, the data-plane slice, bind grant or CatalogEntry.
	// Templates never fall back to tenant-local CRD storage while waiting.
	if _, err := WaitForCachedResourceIdentity(ctx, opts.Config); err != nil {
		return fmt.Errorf("CachedResource identityHash not ready (templates require virtual storage): %w", err)
	}
	return nil
}
