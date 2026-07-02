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

	sdkinstall "github.com/faroshq/provider-sdk/install"
	"github.com/kcp-dev/multicluster-provider/apiexport"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"
	"k8s.io/klog/v2"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"
	mcmanager "sigs.k8s.io/multicluster-runtime/pkg/manager"

	"github.com/faroshq/provider-databricks/backend"
	"github.com/faroshq/provider-databricks/controller/connection"
	"github.com/faroshq/provider-databricks/controller/table"
	"github.com/faroshq/provider-databricks/controller/warehouse"
	databricksscheme "github.com/faroshq/provider-databricks/scheme"
)

func startControllerManager(ctx context.Context, config *rest.Config, validator backend.Validator) error {
	if config == nil {
		return errControllerDisabled
	}
	ctrl.SetLogger(klog.NewKlogr())
	scheme := databricksscheme.NewScheme()

	dyn, err := dynamic.NewForConfig(config)
	if err != nil {
		return fmt.Errorf("dynamic client: %w", err)
	}
	workspacePath := envOr("DATABRICKS_WORKSPACE_PATH", defaultWorkspacePath)
	if err := sdkinstall.EnsureAPIExportEndpointSlice(ctx, dyn, apiExportName, apiExportName, workspacePath); err != nil {
		log.Printf("controller manager: WARNING could not ensure APIExportEndpointSlice: %v", err)
	}

	provider, err := apiexport.New(config, apiExportName, apiexport.Options{Scheme: scheme})
	if err != nil {
		return fmt.Errorf("creating apiexport multicluster provider: %w", err)
	}
	mgr, err := mcmanager.New(config, provider, manager.Options{
		Scheme:  scheme,
		Metrics: metricsserver.Options{BindAddress: "0"},
	})
	if err != nil {
		return fmt.Errorf("creating multicluster manager: %w", err)
	}
	if err := (&connection.Reconciler{Validator: validator}).SetupWithManager(mgr); err != nil {
		return fmt.Errorf("connection controller: %w", err)
	}
	if err := (&warehouse.Reconciler{Validator: validator}).SetupWithManager(mgr); err != nil {
		return fmt.Errorf("warehouse controller: %w", err)
	}
	if err := (&table.Reconciler{Validator: validator}).SetupWithManager(mgr); err != nil {
		return fmt.Errorf("table controller: %w", err)
	}
	go func() {
		log.Printf("databricks controller manager starting (endpointSlice=%s)", apiExportName)
		if err := mgr.Start(ctx); err != nil {
			log.Printf("controller manager exited: %v", err)
		}
	}()
	return nil
}
