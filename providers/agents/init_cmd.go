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
	schemasDir := os.Getenv("RAILGRID_SCHEMAS_DIR")
	if schemasDir == "" {
		schemasDir = "/etc/railgrid/schemas"
	}
	catalogEntryFile := os.Getenv("RAILGRID_CATALOGENTRY_FILE")

	if err := sdkinstall.Bootstrap(ctx, sdkinstall.Options{
		Config:        config,
		ExportName:    apiExportName,
		WorkspacePath: workspacePath,
		SchemasDir:    schemasDir,
		// The provider stores model credentials and per-connection secrets in
		// the tenant workspace and acts as the calling user; the claim lets it
		// read/write those Secrets. Tenant scoping is expressed in the
		// CatalogEntry's permissionClaims (manifest.yaml).
		//
		// MUST stay in sync with manifest.yaml and the chart's
		// catalogentry.yaml: the CatalogEntry drives what tenants accept on
		// their APIBinding, but the virtual workspace authorizes against the
		// claims on the APIExport written here — a claim missing on either
		// side is denied.
		Claims: []sdkinstall.PermissionClaim{
			{Resource: "secrets", Verbs: []string{"get", "list", "watch", "create", "update", "delete"}},
			// Per-agent identity for background runs (api/agentidentity.go):
			// the agent's ServiceAccount, its read-instances ClusterRole and
			// binding. Create-only so the provider can never widen a grant it
			// once made.
			{Resource: "serviceaccounts", Verbs: []string{"get", "create"}},
			{Group: "rbac.authorization.k8s.io", Resource: "clusterroles", Verbs: []string{"get", "create"}},
			{Group: "rbac.authorization.k8s.io", Resource: "clusterrolebindings", Verbs: []string{"get", "create"}},
			// Service-to-service invocation (api/s2s.go). A caller that is not
			// a human — another provider, a job — presents its own
			// ServiceAccount token instead of a user's, so the provider
			// authenticates and authorizes it itself: TokenReview to resolve
			// the identity, SubjectAccessReview to check it may run this agent,
			// both on the APIExport virtual workspace scoped to the target
			// cluster (kcp#4279 / kcp#4280).
			//
			// These are built-in kubernetes API groups, so they carry no
			// IdentityHash: kcp only requires one for a claim on a non-built-in
			// type served by another APIExport (see PermissionClaim in
			// provider-sdk/install/install.go). Verb create only — a review is
			// a POST of a question, there is nothing to get or list.
			{Group: "authentication.k8s.io", Resource: "tokenreviews", Verbs: []string{"create"}},
			{Group: "authorization.k8s.io", Resource: "subjectaccessreviews", Verbs: []string{"create"}},
		},
		CatalogEntryFile: catalogEntryFile,
	}); err != nil {
		return fmt.Errorf("provider workspace bootstrap: %w", err)
	}
	log.Printf("agents-provider init: workspace bootstrapped (export=%s path=%s schemas=%s catalogEntry=%s)", apiExportName, workspacePath, schemasDir, catalogEntryFile)
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
