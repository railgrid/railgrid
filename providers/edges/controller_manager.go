// Copyright 2026 The Faros Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package main

// Edge controller manager — reconciles Edge CRs across tenant workspaces via a
// kcp APIExport multicluster provider (watches the provider's
// APIExportEndpointSlice and engages each tenant logical cluster that bound the
// edges APIExport). Relocated from the hub's pkg/hub/controllers/edge.

import (
	"context"
	"errors"
	"fmt"
	"log"

	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"
	"k8s.io/klog/v2"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	"github.com/kcp-dev/multicluster-provider/apiexport"
	mcmanager "sigs.k8s.io/multicluster-runtime/pkg/manager"

	edgectrl "github.com/faroshq/provider-edges/internal/edgectrl"
	"github.com/faroshq/provider-edges/internal/scheduler"
	"github.com/faroshq/provider-edges/internal/status"
	sdkinstall "github.com/faroshq/provider-sdk/install"

	edgesv1alpha1 "github.com/faroshq/provider-edges/apis/v1alpha1"
	edgescheme "github.com/faroshq/provider-edges/scheme"
)

// errControllerDisabled is the sentinel main() checks for so it can log +
// continue without the manager when no kubeconfig is in scope.
var errControllerDisabled = errors.New("no kubeconfig available; edge controller manager disabled")

// endpointSliceName is the APIExportEndpointSlice the multicluster provider
// watches. By convention (provider-sdk) the slice name equals the APIExport
// name — see sdkinstall.Bootstrap / EnsureAPIExportEndpointSlice.
const endpointSliceName = apiExportName

// startEdgeControllerManager builds the multicluster manager and starts the
// edge token / RBAC / lifecycle reconcilers. connManager wires the lifecycle
// reconciler's tunnel-liveness cross-check to the provider's live ConnManager.
// A nil config means "skip the manager" (healthz-only / dev).
func startEdgeControllerManager(ctx context.Context, config *rest.Config, connManager edgectrl.ConnManager, hubExternalURL string, hubCAData []byte, devMode bool) error {
	if config == nil {
		return errControllerDisabled
	}

	ctrl.SetLogger(klog.NewKlogr())
	s := edgescheme.NewScheme()

	// The hub provisioner does not create the APIExportEndpointSlice for the
	// provider's APIExport, so ensure it here (idempotent) before building the
	// multicluster provider. Best-effort: log + continue; the manager engages
	// no clusters until the slice lands.
	dynCl, err := dynamic.NewForConfig(config)
	if err != nil {
		return fmt.Errorf("dynamic client: %w", err)
	}
	if err := sdkinstall.EnsureAPIExportEndpointSlice(ctx, dynCl, endpointSliceName, apiExportName, defaultWorkspacePath); err != nil {
		log.Printf("edge controller manager: WARNING could not ensure APIExportEndpointSlice: %v", err)
	}

	provider, err := apiexport.New(config, endpointSliceName, apiexport.Options{Scheme: s})
	if err != nil {
		return fmt.Errorf("creating apiexport multicluster provider: %w", err)
	}

	mgr, err := mcmanager.New(config, provider, manager.Options{
		Scheme:  s,
		Metrics: metricsserver.Options{BindAddress: "0"}, // provider serves its own HTTP
	})
	if err != nil {
		return fmt.Errorf("creating multicluster manager: %w", err)
	}

	opts := edgectrl.Options{HubExternalURL: hubExternalURL, HubCAData: hubCAData, DevMode: devMode}
	// One set of token/RBAC/lifecycle controllers per kind, on the shared
	// multicluster manager. Both kinds share the single tunnel ConnManager (keyed
	// by resource/cluster/name), so the lifecycle reconciler's tunnel-liveness
	// cross-check works for either.
	if err := edgectrl.SetupControllers(mgr,
		edgesv1alpha1.KubernetesClusterGVR, "KubernetesCluster", edgesv1alpha1.NewKubernetesCluster,
		connManager, opts,
	); err != nil {
		return fmt.Errorf("KubernetesCluster controllers: %w", err)
	}
	if err := edgectrl.SetupControllers(mgr,
		edgesv1alpha1.LinuxServerGVR, "LinuxServer", edgesv1alpha1.NewLinuxServer,
		connManager, opts,
	); err != nil {
		return fmt.Errorf("LinuxServer controllers: %w", err)
	}

	// Workload scheduling (KubernetesCluster edges only): the scheduler fans a
	// Workload out into one Placement per matching edge; the status
	// aggregator rolls per-edge Placement statuses back up. Each edge's agent
	// applies the derived Deployment locally and reports Placement status.
	if err := scheduler.SetupWithManager(mgr); err != nil {
		return fmt.Errorf("Workload scheduler: %w", err)
	}
	if err := status.SetupWithManager(mgr); err != nil {
		return fmt.Errorf("Workload status aggregator: %w", err)
	}

	go func() {
		log.Printf("edges controller manager starting (endpointSlice=%s)", endpointSliceName)
		if err := mgr.Start(ctx); err != nil {
			log.Printf("edge controller manager exited: %v", err)
		}
	}()
	return nil
}
