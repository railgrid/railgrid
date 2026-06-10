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

	"github.com/faroshq/faros-kedge/providers/code/install"
)

// runInitCmd is the one-shot bootstrap. The hub provisioner already
// materializes everything the CatalogEntry declares (sub-workspace, the four
// APIResourceSchemas, the APIExport, the provider ServiceAccount + minted
// kubeconfig). The one thing it does NOT create is an APIExportEndpointSlice
// for code.providers.kedge.faros.sh — without it the multicluster manager has
// no endpoints to watch. init creates it.
//
// serve also ensures the slice idempotently at startup, so running init
// separately is optional; it exists for parity with the infrastructure
// provider's init/serve split and for environments that bootstrap out-of-band.
func runInitCmd(ctx context.Context) error {
	config, err := loadControllerConfig()
	if err != nil {
		return fmt.Errorf("init needs a kubeconfig (set CODE_KUBECONFIG): %w", err)
	}
	workspacePath := os.Getenv("CODE_WORKSPACE_PATH")
	if workspacePath == "" {
		workspacePath = defaultWorkspacePath
	}
	if err := install.EnsureAPIExportEndpointSlice(ctx, config, workspacePath); err != nil {
		return fmt.Errorf("ensure APIExportEndpointSlice: %w", err)
	}
	log.Printf("code-provider init: APIExportEndpointSlice ensured for %s (path %s)", install.APIExportName, workspacePath)
	return nil
}
