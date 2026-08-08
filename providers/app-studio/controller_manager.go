/*
Copyright 2026 The Faros Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package main

// Multicluster controller manager — reconciles Project CRs across EVERY
// tenant workspace that has bound this provider's APIExport, via the kcp
// apiexport multicluster provider. The library watches the provider's
// APIExportEndpointSlice and engages one wildcard watcher PER SHARD (the
// slice advertises one endpoint per kcp shard — binding a single URL would
// silently hide every tenant on the other shards).
//
// This is where the deterministic lifecycle lives: the HTTP layer only
// writes Project spec; the reconciler converges infrastructure instances and
// mirrors their status back. OPT-IN via KEDGE_PROVIDER_KUBECONFIG — without
// it the provider runs REST/portal-only.

import (
	"context"
	"errors"
	"fmt"
	"log"

	"k8s.io/client-go/rest"
	"k8s.io/klog/v2"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	"github.com/kcp-dev/multicluster-provider/apiexport"
	mcmanager "sigs.k8s.io/multicluster-runtime/pkg/manager"

	"github.com/faroshq/provider-app-studio/controller/project"
	"github.com/faroshq/provider-app-studio/controller/session"
	"github.com/faroshq/provider-app-studio/controller/studio"
	appscheme "github.com/faroshq/provider-app-studio/scheme"
	"github.com/faroshq/provider-app-studio/store"
	"github.com/faroshq/provider-app-studio/workspace"
)

// endpointSliceName matches the provider's APIExport name by convention
// (sdkinstall.Bootstrap creates the slice under the same name at init).
const endpointSliceName = apiExportName

// errControllerDisabled is the sentinel runServe checks so it can log +
// continue without the manager when no kubeconfig is in scope.
var errControllerDisabled = errors.New("no kubeconfig available; controller manager disabled")

// controllerDeps carries the runtime collaborators the Project reconciler
// shares with the HTTP layer: the on-disk workspace store (commit
// convergence reads it), the assistant-busy gate, and the hub address for
// MCP commit calls.
type controllerDeps struct {
	Workspace   *workspace.FileStore
	Busy        func(workspace.Scope) bool
	Store       store.Store
	HubBase     string
	HubInsecure bool
}

// startControllerManager builds the multicluster manager and starts the
// Project reconciler. A nil config means "skip the manager, run REST-only".
func startControllerManager(ctx context.Context, config *rest.Config, deps controllerDeps) error {
	if config == nil {
		return errControllerDisabled
	}

	ctrl.SetLogger(klog.NewKlogr())
	scheme := appscheme.NewScheme()

	provider, err := apiexport.New(config, endpointSliceName, apiexport.Options{Scheme: scheme})
	if err != nil {
		return fmt.Errorf("creating apiexport multicluster provider: %w", err)
	}

	mgr, err := mcmanager.New(config, provider, manager.Options{
		Scheme:  scheme,
		Metrics: metricsserver.Options{BindAddress: "0"}, // provider serves its own HTTP; disable controller-runtime metrics
	})
	if err != nil {
		return fmt.Errorf("creating multicluster manager: %w", err)
	}

	if err := (&project.Reconciler{
		Workspace:   deps.Workspace,
		Busy:        deps.Busy,
		HubBase:     deps.HubBase,
		HubInsecure: deps.HubInsecure,
	}).SetupWithManager(mgr); err != nil {
		return fmt.Errorf("project controller: %w", err)
	}
	if err := (&session.Reconciler{Store: deps.Store}).SetupWithManager(mgr); err != nil {
		return fmt.Errorf("session controller: %w", err)
	}
	if err := (&studio.Reconciler{}).SetupWithManager(mgr); err != nil {
		return fmt.Errorf("studio controller: %w", err)
	}

	go func() {
		log.Printf("app-studio controller manager starting (endpointSlice=%s)", endpointSliceName)
		if err := mgr.Start(ctx); err != nil {
			log.Printf("controller manager exited: %v", err)
		}
	}()
	return nil
}
