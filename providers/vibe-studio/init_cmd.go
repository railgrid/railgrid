// Copyright 2026 The Faros Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//	http://www.apache.org/licenses/LICENSE-2.0

package main

import (
	"context"
	"fmt"
	"log"
	"os"

	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"

	sdkinstall "github.com/faroshq/provider-sdk/install"
)

const (
	apiExportName        = "vibe.faros.sh"
	defaultWorkspacePath = "root:faros:providers:vibe-studio"
)

// instanceClaimResources are the infrastructure resources the Project
// reconciler lifecycles. The flattened API serves every template's instances
// as ONE resource (instances.infrastructure.faros.sh, product selected by
// spec.template), so this list never grows with the template vocabulary.
var instanceClaimResources = []string{"instances"}

// runInitCmd applies the provider's in-workspace objects (APIResourceSchemas,
// APIExport, APIExportEndpointSlice, bind grant) using the workspace-admin
// kubeconfig the admin onboarded. Idempotent.
//
// The permission claims cover the infrastructure instance resources the
// Project reconciler creates/deletes in tenant workspaces. They are
// first-party (*.faros.sh) types, so kcp requires the identityHash of the
// APIExport that serves them (infrastructure.providers.faros.sh) —
// supplied via VIBE_STUDIO_INFRA_IDENTITY_HASH (Helm value in prod; the dev
// Makefile auto-discovers it). Template catalog reads need NO claim: the
// wizard reads Templates as the calling user via the hub gateway.
func runInitCmd(ctx context.Context) error {
	config, err := loadInitConfig()
	if err != nil {
		return fmt.Errorf("init needs a kubeconfig (set FAROS_PROVIDER_KUBECONFIG): %w", err)
	}
	workspacePath := os.Getenv("VIBE_STUDIO_WORKSPACE_PATH")
	if workspacePath == "" {
		workspacePath = defaultWorkspacePath
	}
	schemasDir := os.Getenv("FAROS_SCHEMAS_DIR")
	if schemasDir == "" {
		schemasDir = "/etc/faros/schemas"
	}
	catalogEntryFile := os.Getenv("FAROS_CATALOGENTRY_FILE")

	infraHash := os.Getenv("VIBE_STUDIO_INFRA_IDENTITY_HASH")
	if infraHash == "" {
		log.Printf("WARNING: VIBE_STUDIO_INFRA_IDENTITY_HASH is empty — the instance permission claims will have no identityHash and tenant Enable will not engage instance lifecycling")
	}
	claims := make([]sdkinstall.PermissionClaim, 0, len(instanceClaimResources)+1)
	for _, r := range instanceClaimResources {
		claims = append(claims, sdkinstall.PermissionClaim{
			Group:        "infrastructure.faros.sh",
			Resource:     r,
			Verbs:        []string{"get", "list", "watch", "create", "update", "patch", "delete"},
			IdentityHash: infraHash,
		})
	}
	// Repository creation (git seeding) — the reconciler creates Repository
	// CRs; the seed commit itself goes through the code provider's MCP as
	// the caller (commit bundles are code-provider-local).
	codeHash := os.Getenv("VIBE_STUDIO_CODE_IDENTITY_HASH")
	if codeHash == "" {
		log.Printf("WARNING: VIBE_STUDIO_CODE_IDENTITY_HASH is empty — the repositories claim will have no identityHash and the reconciler cannot create repositories")
	}
	claims = append(claims, sdkinstall.PermissionClaim{
		Group:        "code.faros.sh",
		Resource:     "repositories",
		Verbs:        []string{"get", "list", "watch", "create", "update", "patch"},
		IdentityHash: codeHash,
	})
	// Per-session ServiceAccount identity: provisioning runs in the Session
	// reconciler long after the approving request, so it acts as an identity
	// of its own rather than borrowing the user's bearer. These are built-in
	// types — no identityHash needed.
	claims = append(claims,
		sdkinstall.PermissionClaim{Resource: "serviceaccounts", Verbs: []string{"get", "list", "watch", "create", "delete"}},
		sdkinstall.PermissionClaim{Resource: "secrets", Verbs: []string{"get", "list", "watch", "create", "update", "delete"}},
		sdkinstall.PermissionClaim{
			Group:    "rbac.authorization.k8s.io",
			Resource: "clusterroles",
			Verbs:    []string{"get", "list", "watch", "create", "update", "delete"},
		},
		sdkinstall.PermissionClaim{
			Group:    "rbac.authorization.k8s.io",
			Resource: "clusterrolebindings",
			Verbs:    []string{"get", "list", "watch", "create", "update", "delete"},
		},
	)

	if err := sdkinstall.Bootstrap(ctx, sdkinstall.Options{
		Config:           config,
		ExportName:       apiExportName,
		WorkspacePath:    workspacePath,
		SchemasDir:       schemasDir,
		Claims:           claims,
		CatalogEntryFile: catalogEntryFile,
	}); err != nil {
		return fmt.Errorf("provider workspace bootstrap: %w", err)
	}
	log.Printf("vibe-studio-provider init: workspace bootstrapped (export=%s path=%s schemas=%s catalogEntry=%s claims=%d)", apiExportName, workspacePath, schemasDir, catalogEntryFile, len(claims))
	return nil
}

// loadInitConfig resolves the workspace-admin kubeconfig for init.
func loadInitConfig() (*rest.Config, error) {
	if p := os.Getenv("FAROS_PROVIDER_KUBECONFIG"); p != "" {
		return clientcmd.BuildConfigFromFlags("", p)
	}
	if p := os.Getenv("KUBECONFIG"); p != "" {
		return clientcmd.BuildConfigFromFlags("", p)
	}
	return rest.InClusterConfig()
}
