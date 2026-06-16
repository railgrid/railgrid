// Copyright 2026 The Faros Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package main

// Multicluster controller manager — reconciles the code provider's
// tenant-authored CRs (Connection / Repository / RepositoryCommit / DeployKey / Collaborator)
// across EVERY tenant workspace that has bound this provider's APIExport.
//
// Unlike the infrastructure provider (a single-cluster manager over its own
// workspace), the code provider's CRs live in tenant workspaces, so we use the
// kcp apiexport multicluster provider: it watches the provider's
// APIExportEndpointSlice and engages each tenant logical cluster. Each
// reconciler resolves a per-tenant client from req.ClusterName.
//
// OPT-IN via CODE_KUBECONFIG (or the standard KUBECONFIG fallback). When no
// kubeconfig is in scope the provider runs REST/MCP-only (no controller),
// keeping the dev/portal flow intact.

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"

	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/klog/v2"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	"github.com/kcp-dev/multicluster-provider/apiexport"
	mcmanager "sigs.k8s.io/multicluster-runtime/pkg/manager"

	"github.com/faroshq/provider-code/backend"
	"github.com/faroshq/provider-code/commitbundle"
	"github.com/faroshq/provider-code/controller/collaborator"
	"github.com/faroshq/provider-code/controller/connection"
	"github.com/faroshq/provider-code/controller/deploykey"
	"github.com/faroshq/provider-code/controller/packages"
	"github.com/faroshq/provider-code/controller/repository"
	"github.com/faroshq/provider-code/controller/repositorycommit"
	"github.com/faroshq/provider-code/install"
	codescheme "github.com/faroshq/provider-code/scheme"
)

// endpointSliceName is the APIExportEndpointSlice the multicluster provider
// watches to discover tenant workspaces. By convention it matches the
// provider's APIExport name (manifest.yaml spec.apiExport.name).
const endpointSliceName = install.APIExportEndpointSliceName

// defaultWorkspacePath is the kcp logical-cluster path the provider's APIExport
// lives in (root:kedge:providers:<name>). Overridable via CODE_WORKSPACE_PATH.
const defaultWorkspacePath = "root:kedge:providers:code"

// startControllerManager builds the multicluster manager and starts the
// reconcilers, dispatching through the shared backend registry (built in
// runServe so the HTTP packages handler shares it). A nil config means "skip
// the manager, run REST/MCP-only".
func startControllerManager(ctx context.Context, config *rest.Config, registry *backend.Registry, bundles commitbundle.Store) error {
	if config == nil {
		return errControllerDisabled
	}

	ctrl.SetLogger(klog.NewKlogr())
	scheme := codescheme.NewScheme()

	// The hub provisioner does NOT create an APIExportEndpointSlice for the
	// provider's APIExport, so the multicluster provider would have nothing to
	// watch. Ensure it here (idempotent) before building the provider. Best
	// effort: log and continue if it fails — serve still offers MCP/portal, and
	// the manager simply engages no clusters until the slice lands.
	workspacePath := os.Getenv("CODE_WORKSPACE_PATH")
	if workspacePath == "" {
		workspacePath = defaultWorkspacePath
	}
	if err := install.EnsureAPIExportEndpointSlice(ctx, config, workspacePath); err != nil {
		log.Printf("controller manager: WARNING could not ensure APIExportEndpointSlice: %v", err)
	}

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

	if err := (&connection.Reconciler{Backends: registry}).SetupWithManager(mgr); err != nil {
		return fmt.Errorf("connection controller: %w", err)
	}
	if err := (&repository.Reconciler{Backends: registry}).SetupWithManager(mgr); err != nil {
		return fmt.Errorf("repository controller: %w", err)
	}
	if err := (&repositorycommit.Reconciler{Backends: registry, Bundles: bundles}).SetupWithManager(mgr); err != nil {
		return fmt.Errorf("repositorycommit controller: %w", err)
	}
	if err := (&deploykey.Reconciler{Backends: registry}).SetupWithManager(mgr); err != nil {
		return fmt.Errorf("deploykey controller: %w", err)
	}
	if err := (&collaborator.Reconciler{Backends: registry}).SetupWithManager(mgr); err != nil {
		return fmt.Errorf("collaborator controller: %w", err)
	}
	if err := (&packages.Reconciler{Backends: registry}).SetupWithManager(mgr); err != nil {
		return fmt.Errorf("packages controller: %w", err)
	}

	go func() {
		log.Printf("code controller manager starting (backends=%v, endpointSlice=%s)", registry.Names(), endpointSliceName)
		if err := mgr.Start(ctx); err != nil {
			log.Printf("controller manager exited: %v", err)
		}
	}()
	return nil
}

// loadControllerConfig resolves the rest.Config for the provider's kcp
// workspace, in order:
//
//	CODE_KUBECONFIG  — minted SA kubeconfig from `init` / the hub
//	KUBECONFIG       — standard env var
//	in-cluster SA    — when run as a pod
//
// Returns errControllerDisabled when none resolve.
func loadControllerConfig() (*rest.Config, error) {
	// KEDGE_PROVIDER_KUBECONFIG is the standardized name across all providers.
	// CODE_KUBECONFIG is kept as a fallback for one release.
	if p := os.Getenv("KEDGE_PROVIDER_KUBECONFIG"); p != "" {
		c, err := clientcmd.BuildConfigFromFlags("", p)
		if err != nil {
			return nil, fmt.Errorf("KEDGE_PROVIDER_KUBECONFIG: %w", err)
		}
		return c, nil
	}
	if p := os.Getenv("CODE_KUBECONFIG"); p != "" {
		c, err := clientcmd.BuildConfigFromFlags("", p)
		if err != nil {
			return nil, fmt.Errorf("CODE_KUBECONFIG: %w", err)
		}
		return c, nil
	}
	if p := os.Getenv("KUBECONFIG"); p != "" {
		c, err := clientcmd.BuildConfigFromFlags("", p)
		if err != nil {
			return nil, fmt.Errorf("KUBECONFIG: %w", err)
		}
		return c, nil
	}
	c, err := rest.InClusterConfig()
	if err != nil {
		return nil, errControllerDisabled
	}
	return c, nil
}

// errControllerDisabled is the sentinel main() checks so it can log + continue
// without the manager when no kubeconfig is in scope.
var errControllerDisabled = errors.New("no kubeconfig available; controller manager disabled")
