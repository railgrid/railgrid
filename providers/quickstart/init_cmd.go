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

	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"

	sdkinstall "github.com/railgrid/provider-sdk/install"
)

const (
	apiExportName = "quickstart.providers.railgrid.ai"
)

// runInitCmd applies the provider's in-workspace objects (APIResourceSchemas,
// APIExport, APIExportEndpointSlice, bind grant) using the workspace-admin
// kubeconfig the admin onboarded. Idempotent.
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
	workspacePath := os.Getenv("QUICKSTART_WORKSPACE_PATH")
	kcpDir := os.Getenv("RAILGRID_KCP_DIR")
	if kcpDir == "" {
		kcpDir = "/etc/railgrid/kcp"
	}
	// CatalogEntry self-registration: the provider applies its own CatalogEntry
	// into its workspace (the hub watches it there). Empty → skip. The same
	// lookup serve uses to derive its custom-subresource routes (catalogentry.go),
	// so init and serve can never read a different declaration.
	catalogEntryFile := catalogEntryPath()
	// Where kcp reverse-proxies a custom subresource request to. Empty means
	// "spec.backend.url of the CatalogEntry above", which is right whenever the
	// chart runs init; a harness that registers the CatalogEntry itself (the
	// provider e2e does) has no file to read it from and sets this instead.
	dataPlaneURL := strings.TrimSpace(os.Getenv("RAILGRID_DATAPLANE_URL"))

	// Bootstrap applies, idempotently and in order: the APIResourceSchemas in
	// KCPDir, the generated APIExport beside them, the APIExportEndpointSlice
	// the controller manager watches to discover tenant workspaces, and the
	// bind grant that lets tenants create an APIBinding. Without the slice the
	// multicluster manager has nothing to watch and no Greeting anywhere is
	// ever reconciled, so this one call is what makes the controller real.
	//
	// There is no claim list here, and there is no place to put one: a
	// permission claim is written in manifest.yaml and nowhere else, codegen
	// turns it into deploy/chart/files/apiexport.yaml, and init applies that
	// file as it stands. This provider claims no data — it reconciles only the
	// Greetings its own APIExport serves; its one claim is the built-in
	// subjectaccessreviews review API the greet subresource's proxied gate runs
	// through the export virtual workspace.
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
	log.Printf("quickstart-provider init: workspace bootstrapped (export=%s path=%s kcpDir=%s catalogEntry=%s)", apiExportName, workspacePath, kcpDir, catalogEntryFile)
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
