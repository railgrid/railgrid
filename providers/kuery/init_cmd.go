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
	apiExportName        = "kuery.providers.faros.sh"
)

// runInitCmd applies kuery's in-workspace objects (APIResourceSchemas,
// APIExport, APIExportEndpointSlice, bind grant) using the workspace-admin
// kubeconfig the admin onboarded. Idempotent.
//
// kuery's APIExport deliberately claims NO first-party (*.faros.sh)
// resources. Such a claim must pin the serving APIExport's identityHash, and
// an export can pin exactly one identity per claimed resource — for every
// consuming workspace at once — which breaks the moment one org self-hosts
// the edges provider while others use the platform copy. Edge discovery
// instead acts as a per-workspace ServiceAccount through each workspace's
// own edges binding (see engagement + provider-sdk/tenantaccess). Only
// built-in types (no identityHash) are claimed, to provision that identity.
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
	workspacePath := os.Getenv("KUERY_WORKSPACE_PATH")
	schemasDir := os.Getenv("FAROS_SCHEMAS_DIR")
	if schemasDir == "" {
		schemasDir = "/etc/faros/schemas"
	}

	catalogEntryFile := os.Getenv("FAROS_CATALOGENTRY_FILE")

	if err := sdkinstall.Bootstrap(ctx, sdkinstall.Options{
		Config:        config,
		ExportName:    apiExportName,
		WorkspacePath: workspacePath,
		SchemasDir:    schemasDir,
		// Built-in types backing the per-workspace engagement ServiceAccount
		// (see engagement.ensureIdentity). Keep in sync with manifest.yaml +
		// deploy/chart/templates/catalogentry.yaml — the hub writes the
		// tenant APIBinding claims from those at Enable time, and a claim
		// missing there is silently denied at reconcile.
		Claims: []sdkinstall.PermissionClaim{
			{Resource: "serviceaccounts", Verbs: []string{"get", "list", "watch", "create"}},
			{Resource: "secrets", Verbs: []string{"get", "list", "watch", "create"}},
			{Group: "rbac.authorization.k8s.io", Resource: "clusterroles", Verbs: []string{"get", "list", "watch", "create"}},
			{Group: "rbac.authorization.k8s.io", Resource: "clusterrolebindings", Verbs: []string{"get", "list", "watch", "create"}},
		},
		CatalogEntryFile: catalogEntryFile,
	}); err != nil {
		return fmt.Errorf("provider workspace bootstrap: %w", err)
	}
	log.Printf("kuery init: workspace bootstrapped (export=%s path=%s schemas=%s catalogEntry=%s)", apiExportName, workspacePath, schemasDir, catalogEntryFile)
	return nil
}
