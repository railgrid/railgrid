// Copyright 2026 The Faros Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package main

// Operator mode — the recommended way to run this provider.
//
// Instead of the init-container / hub-mint / token-mint / Secret-handoff
// dance, the operator is given exactly two kubeconfigs and does the rest:
//
//	INFRASTRUCTURE_PROVIDER_KUBECONFIG  kcp, scoped to the provider workspace
//	                                    (root:kedge:providers:infrastructure).
//	                                    What the kedge admin portal issues.
//	INFRASTRUCTURE_RUNTIME_KUBECONFIG   the cluster where kro (and this
//	                                    operator) run. Used to seed kro.
//
// One process does both halves:
//
//  1. A continuous, self-healing reconcile loop that ensures the provider
//     workspace bootstrap (CRDs, APIExport, CachedResource, EndpointSlice,
//     APIExportEndpointSlice, schemas, seed Templates) and seeds kro's
//     kcp-kubeconfig Secret on the runtime cluster — every step idempotent.
//  2. The serve loop (HTTP/MCP/controller manager) on the provider kubeconfig.
//
// No ServiceAccount minting, no runtime-kubeconfig Secret relay: the provider
// kubeconfig IS the credential, for both the operator and (copied across) kro.

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"

	"github.com/faroshq/provider-infrastructure/install"
	"github.com/faroshq/provider-infrastructure/operator"
)

// operatorReconcileInterval is how often the bootstrap reconcile re-runs.
// Every step is idempotent, so this is purely a self-healing cadence —
// e.g. it re-seeds kro if the Secret is deleted, or finishes APIExport
// wiring once a slow CachedResource identityHash settles.
const operatorReconcileInterval = 60 * time.Second

// runOperator is the entrypoint for `infrastructure operator`.
func runOperator() error {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	providerCfg, providerKubeconfig, err := loadOperatorProviderConfig()
	if err != nil {
		return fmt.Errorf("provider kubeconfig: %w", err)
	}

	runtimeCfg, runtimePath, err := loadOperatorRuntimeConfig()
	if err != nil {
		return fmt.Errorf("runtime kubeconfig: %w", err)
	}
	if runtimeCfg == nil {
		log.Printf("operator: INFRASTRUCTURE_RUNTIME_KUBECONFIG unset — kro will not be seeded; tenant Instance CRs stay Pending until it is")
	} else {
		// The controller manager's kro backend authors RGDs on the runtime
		// cluster; point it there unless the caller already set KRO_KUBECONFIG.
		if os.Getenv("KRO_KUBECONFIG") == "" && runtimePath != "" {
			_ = os.Setenv("KRO_KUBECONFIG", runtimePath)
		}
	}

	// Skip the controller manager's own bootstrap path: the reconcile loop
	// below owns all the install steps.
	if os.Getenv("INFRASTRUCTURE_KUBECONFIG") == "" {
		if p := os.Getenv("INFRASTRUCTURE_PROVIDER_KUBECONFIG"); p != "" {
			_ = os.Setenv("INFRASTRUCTURE_KUBECONFIG", p)
		}
	}

	// Reconcile loop runs in the background; serve blocks in the foreground.
	go runBootstrapLoop(ctx, providerCfg, runtimeCfg, providerKubeconfig)

	log.Printf("operator: starting serve loop on provider kubeconfig")
	serveWithConfig(ctx, providerCfg)
	return nil
}

// runBootstrapLoop reconciles the provider-workspace bootstrap (and kro seed)
// immediately, then on operatorReconcileInterval, until ctx is cancelled. A
// failed pass is logged and retried on the next tick — the whole point of the
// loop vs. a one-shot init.
func runBootstrapLoop(ctx context.Context, providerCfg, runtimeCfg *rest.Config, providerKubeconfig []byte) {
	reconcile := func() {
		if err := bootstrapOnce(ctx, providerCfg, runtimeCfg, providerKubeconfig); err != nil {
			log.Printf("operator: bootstrap reconcile failed (will retry in %s): %v", operatorReconcileInterval, err)
			return
		}
		log.Printf("operator: bootstrap reconcile OK")
	}

	reconcile()
	ticker := time.NewTicker(operatorReconcileInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			reconcile()
		}
	}
}

// bootstrapOnce runs one idempotent pass of the provider-workspace bootstrap
// (shared with the CRD operator via operator.Bootstrap), then seeds kro on the
// runtime cluster from the provider kubeconfig. This is the env-driven path —
// the CRD operator (`controller` subcommand) does the same steps per CR.
func bootstrapOnce(ctx context.Context, providerCfg, runtimeCfg *rest.Config, providerKubeconfig []byte) error {
	workspacePath := os.Getenv("INFRASTRUCTURE_WORKSPACE_PATH")
	if err := operator.Bootstrap(ctx, providerCfg, operator.BootstrapOptions{
		WorkspacePath:     workspacePath,
		APIExportName:     apiExportName,
		CatalogEntryFile:  os.Getenv("KEDGE_CATALOGENTRY_FILE"),
		SkipSeedTemplates: os.Getenv("INFRASTRUCTURE_SKIP_SEED_TEMPLATES") != "",
	}); err != nil {
		return err
	}

	if runtimeCfg != nil {
		if err := install.SeedKroClusterFromKubeconfig(ctx, runtimeCfg, providerKubeconfig, workspacePath); err != nil {
			return fmt.Errorf("seed kro: %w", err)
		}
	}
	return nil
}

// runController is the entrypoint for `infrastructure controller` — the
// CRD-driven operator. It builds a controller-runtime manager on the cluster
// the operator runs in (where InfrastructureProvider CRs + their kubeconfig
// Secrets live) and reconciles each CR.
func runController() error {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	cfg, err := loadOperatorManagerConfig()
	if err != nil {
		return fmt.Errorf("operator manager kubeconfig: %w", err)
	}
	return operator.Run(ctx, cfg)
}

// loadOperatorManagerConfig resolves the cluster the operator watches CRs in:
// KUBECONFIG when set (dev/out-of-cluster), else in-cluster.
func loadOperatorManagerConfig() (*rest.Config, error) {
	if p := os.Getenv("KUBECONFIG"); p != "" {
		return clientcmd.BuildConfigFromFlags("", p)
	}
	return rest.InClusterConfig()
}

// loadOperatorProviderConfig resolves the provider (kcp) kubeconfig from
// INFRASTRUCTURE_PROVIDER_KUBECONFIG and returns both the rest.Config and the
// raw bytes (the latter is copied to kro). When INFRASTRUCTURE_WORKSPACE_PATH
// is set the rest.Config Host is retargeted at that workspace, matching how the
// kubeconfig is retargeted before it is handed to kro.
func loadOperatorProviderConfig() (*rest.Config, []byte, error) {
	path := os.Getenv("INFRASTRUCTURE_PROVIDER_KUBECONFIG")
	if path == "" {
		return nil, nil, fmt.Errorf("INFRASTRUCTURE_PROVIDER_KUBECONFIG must be set in operator mode")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, nil, fmt.Errorf("read %s: %w", path, err)
	}
	cfg, err := clientcmd.BuildConfigFromFlags("", path)
	if err != nil {
		return nil, nil, err
	}
	if ws := os.Getenv("INFRASTRUCTURE_WORKSPACE_PATH"); ws != "" {
		host, err := retargetHostToWorkspace(cfg.Host, ws)
		if err != nil {
			return nil, nil, fmt.Errorf("retarget provider kubeconfig to workspace %q: %w", ws, err)
		}
		cfg.Host = host
	}
	return cfg, raw, nil
}

// loadOperatorRuntimeConfig resolves the runtime-cluster kubeconfig from
// INFRASTRUCTURE_RUNTIME_KUBECONFIG. Returns (nil, "", nil) when unset — kro
// seeding is then skipped (the provider still serves).
func loadOperatorRuntimeConfig() (*rest.Config, string, error) {
	path := os.Getenv("INFRASTRUCTURE_RUNTIME_KUBECONFIG")
	if path == "" {
		return nil, "", nil
	}
	cfg, err := clientcmd.BuildConfigFromFlags("", path)
	if err != nil {
		return nil, "", err
	}
	return cfg, path, nil
}
