// Copyright 2026 The Faros Authors.
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

	sdkinstall "github.com/faroshq/provider-sdk/install"
)

const (
	apiExportName        = "ai.faros.sh"
)

// The APIExport deliberately claims NO first-party (*.faros.sh) resources.
// Such claims must pin the serving APIExport's identityHash, and an export
// can pin exactly one identity per claimed resource — for every consuming
// workspace at once. That breaks the moment one org self-hosts a dependency
// (infrastructure, code) while others use the platform copy. Instead the
// reconcilers act as per-project/per-studio ServiceAccounts through each
// workspace's OWN bindings (see package tenantaccess), which reach whichever
// copy the workspace binds. Only built-in types (no identityHash) are
// claimed, to provision those identities.

// runInitCmd applies the App Studio provider's in-workspace objects
// (APIResourceSchemas, APIExport, APIExportEndpointSlice, bind grant) using the
// workspace-admin kubeconfig the admin onboarded. Idempotent.
func runInitCmd(ctx context.Context) error {
	config, err := loadProviderConfig()
	if err != nil {
		return fmt.Errorf("init needs a kubeconfig (set FAROS_PROVIDER_KUBECONFIG): %w", err)
	}
	// Empty means "the workspace this kubeconfig already points at": kcp
	// resolves an unset APIExportEndpointSlice export path to the slice's own
	// logical cluster. Leaving it unset is what lets this one chart bootstrap
	// both the platform workspace and an org's self-hosted copy. Set the env
	// var only to reference an export in a different workspace.
	workspacePath := os.Getenv("APP_STUDIO_WORKSPACE_PATH")
	schemasDir := os.Getenv("FAROS_SCHEMAS_DIR")
	if schemasDir == "" {
		schemasDir = "/etc/faros/schemas"
	}
	catalogEntryFile := os.Getenv("FAROS_CATALOGENTRY_FILE")

	// Per-project/per-studio ServiceAccount identity: instance and repository
	// lifecycling (and repository commits) run in the reconcilers long after
	// the request that caused them, so they act as an identity of their own
	// rather than borrowing the user's bearer. The identity objects are
	// built-in types — no identityHash needed — and the resulting token acts
	// through the workspace's own bindings for everything first-party. Also:
	// per-project LLM credentials ride Secrets.
	claims := make([]sdkinstall.PermissionClaim, 0, 4)
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
	log.Printf("app-studio init: workspace bootstrapped (export=%s path=%s schemas=%s catalogEntry=%s claims=%d)", apiExportName, workspacePath, schemasDir, catalogEntryFile, len(claims))
	return nil
}
