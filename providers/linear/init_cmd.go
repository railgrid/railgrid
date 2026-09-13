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

	"github.com/faroshq/provider-linear/internal/actionapi"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/util/retry"

	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"

	sdkinstall "github.com/faroshq/provider-sdk/install"
)

const (
	apiExportName = "linear.providers.faros.sh"
)

var permissionClaims = []sdkinstall.PermissionClaim{{Resource: "secrets", Verbs: []string{"get"}}}

// runInitCmd applies the provider's in-workspace objects (APIResourceSchemas,
// APIExport, APIExportEndpointSlice, bind grant) using the workspace-admin
// kubeconfig the admin onboarded. Idempotent.
func runInitCmd(ctx context.Context) error {
	config, err := loadInitConfig()
	if err != nil {
		return fmt.Errorf("init needs a kubeconfig (set FAROS_PROVIDER_KUBECONFIG): %w", err)
	}
	if err := actionapi.Install(ctx, config); err != nil {
		return fmt.Errorf("private action storage: %w", err)
	}
	// Empty means "the workspace this kubeconfig already points at": kcp
	// resolves an unset APIExportEndpointSlice export path to the slice's own
	// logical cluster. Leaving it unset is what lets this one chart bootstrap
	// both the platform workspace and an org's self-hosted copy. Set the env
	// var only to reference an export in a different workspace.
	workspacePath := os.Getenv("LINEAR_WORKSPACE_PATH")
	schemasDir := os.Getenv("FAROS_SCHEMAS_DIR")
	if schemasDir == "" {
		schemasDir = "/etc/faros/schemas"
	}
	// CatalogEntry self-registration: the provider applies its own CatalogEntry
	// into its workspace (the hub watches it there). Empty → skip.
	catalogEntryFile := os.Getenv("FAROS_CATALOGENTRY_FILE")

	if err := sdkinstall.Bootstrap(ctx, sdkinstall.Options{
		Config:           config,
		ExportName:       apiExportName,
		WorkspacePath:    workspacePath,
		SchemasDir:       schemasDir,
		Claims:           permissionClaims,
		CatalogEntryFile: catalogEntryFile,
	}); err != nil {
		return fmt.Errorf("provider workspace bootstrap: %w", err)
	}
	client, err := dynamic.NewForConfig(config)
	if err != nil {
		return err
	}
	if err := restrictPublicExport(ctx, client); err != nil {
		return err
	}
	log.Printf("linear-provider init: workspace bootstrapped (export=%s path=%s schemas=%s catalogEntry=%s)", apiExportName, workspacePath, schemasDir, catalogEntryFile)
	return nil
}

// The shared installer preserves prior exports by default. Linear's public
// resource vocabulary is deliberately exactly Connection and Team.
func restrictPublicExport(ctx context.Context, client dynamic.Interface) error {
	resource := client.Resource(schema.GroupVersionResource{Group: "apis.kcp.io", Version: "v1alpha2", Resource: "apiexports"})
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		export, err := resource.Get(ctx, apiExportName, metav1.GetOptions{})
		if err != nil {
			return err
		}
		items, _, err := unstructured.NestedSlice(export.Object, "spec", "resources")
		if err != nil {
			return err
		}
		public := []any{}
		for _, item := range items {
			entry, ok := item.(map[string]any)
			if ok && entry["group"] == apiExportName && (entry["name"] == "connections" || entry["name"] == "teams") {
				public = append(public, item)
			}
		}
		if len(public) != 2 {
			return fmt.Errorf("linear export requires exactly Connection and Team schemas")
		}
		if len(public) == len(items) {
			return nil
		}
		if err := unstructured.SetNestedSlice(export.Object, public, "spec", "resources"); err != nil {
			return err
		}
		_, err = resource.Update(ctx, export, metav1.UpdateOptions{})
		return err
	})
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
