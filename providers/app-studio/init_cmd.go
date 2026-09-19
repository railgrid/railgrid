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
)

const (
	apiExportName = "ai.railgrid.ai"
	// providerName is what this provider's CatalogEntry registers it as, and
	// therefore how it names itself to the hub — in the heartbeat, and in the
	// owner tuple of every identity it asks the hub to mint.
	providerName = "app-studio"
)

// The APIExport deliberately claims NO first-party (*.railgrid.ai) resources,
// and so needs no RAILGRID_IDENTITY_HASHES. Such a claim must pin the serving
// APIExport's identityHash, and an export can pin exactly one identity per
// claimed resource — for every consuming workspace at once. That breaks the
// moment one org self-hosts a dependency (infrastructure, code) while others
// use the platform copy. Instead the reconcilers act as per-project and
// per-Studio identities MINTED BY THE HUB (controller/project/identity.go,
// controller/studio/identity.go) inside each workspace, through that
// workspace's OWN bindings, which reach whichever copy it enabled; what they
// may do there is declared as manifest.yaml spec.dependencies[].composes and
// accepted by the tenant at Enable. The only claim left is on Secrets, for
// the credential material this provider writes itself.

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
	catalogEntryFile := os.Getenv("RAILGRID_CATALOGENTRY_FILE")

	// The per-project and per-Studio identities the reconcilers act as are
	// asked for, not minted here: the hub writes the ServiceAccount, the
	// ClusterRole and the binding against a policy, and collects them when the
	// owning object goes (provider-sdk/identityclient). The one claim left is
	// on Secrets — the credential material this provider writes itself —
	// declared in manifest.yaml, which codegen stamps onto the APIExport this
	// reads. No IdentityHashes: there is no first-party claim to pin.
	if err := sdkinstall.Bootstrap(ctx, sdkinstall.Options{
		Config:           config,
		ExportName:       apiExportName,
		WorkspacePath:    workspacePath,
		KCPDir:           kcpDir,
		CatalogEntryFile: catalogEntryFile,
	}); err != nil {
		return fmt.Errorf("provider workspace bootstrap: %w", err)
	}
	log.Printf("app-studio init: workspace bootstrapped (export=%s path=%s kcpDir=%s catalogEntry=%s)", apiExportName, workspacePath, kcpDir, catalogEntryFile)
	return nil
}
