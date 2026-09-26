// Copyright 2026 The Railgrid Authors.
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
//	                                    (root:railgrid:providers:infrastructure).
//	                                    What the railgrid admin portal issues.
//	INFRASTRUCTURE_RUNTIME_KUBECONFIG   the cluster where kro (and this
//	                                    operator) run. Used to seed kro.
//
// One process does both halves:
//
//  1. A watch-driven, self-healing reconciler that ensures the provider
//     workspace bootstrap (CRDs, APIExport, CachedResource, EndpointSlice,
//     APIExportEndpointSlice, schemas, seed Templates) — every step
//     idempotent, re-run when one of those objects changes, never on a tick.
//  2. The serve loop (HTTP/MCP/controller manager) on the provider kubeconfig.
//
// No ServiceAccount minting, no runtime-kubeconfig Secret relay: the provider
// kubeconfig IS the credential, for both the operator and (copied across) kro.

import (
	"context"
	_ "embed"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"

	"github.com/railgrid/provider-sdk/leaderelection"

	"github.com/railgrid/provider-infrastructure/operator"
)

// catalogEntryManifest is the provider's CatalogEntry, embedded so the operator
// can self-register it into the provider workspace (ui/backend URLs rewritten to
// the serve Service it owns).
//
//go:embed manifest.yaml
var catalogEntryManifest []byte

// Backoff bounds for a failed bootstrap pass. This is the "backing off a
// failed call" requeue of docs/provider-connectivity-contract.md § "Pillar 1
// carve-outs" — the only timer left in operator mode. A successful pass arms
// no timer at all: what re-runs it is an event on the objects it applied
// (operator.WatchProviderWorkspace).
const (
	bootstrapRetryMin = 2 * time.Second
	bootstrapRetryMax = 5 * time.Minute
)

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

	// Serve reads exactly one kubeconfig name. Operator mode hands it the
	// config it already resolved, so nothing is bridged through the
	// environment here.

	// The bootstrap reconciler runs in the background; serve blocks in the
	// foreground. Leader-elected so multi-replica deployments run one
	// reconciler at a time: every step is idempotent, but two replicas
	// applying the same objects is pure conflict churn.
	go func() {
		if err := leaderelection.Run(ctx, leaderelection.Options{
			Config:    providerCfg,
			Namespace: leaderelection.DefaultNamespace,
			Name:      bootstrapLeaseName,
		}, func(termCtx context.Context) {
			runBootstrapReconciler(termCtx, providerCfg, runtimeCfg, providerKubeconfig)
		}); err != nil {
			log.Printf("operator: bootstrap leader election failed; bootstrap reconciler is not running: %v", err)
		}
	}()

	log.Printf("operator: starting serve loop on provider kubeconfig")
	serveWithConfig(ctx, providerCfg)
	return nil
}

// runBootstrapReconciler converges the provider-workspace bootstrap once, then
// re-converges it on events rather than on a tick: as soon as the first pass
// succeeds, the objects it applied (the CatalogEntry, the seed Templates) are
// watched in the provider workspace, and any add, update or delete there
// triggers another pass. A failed pass is retried with exponential backoff —
// the one sanctioned timer here — and a success resets it.
//
// This is the env-driven twin of the CRD operator's Reconcile: same steps, same
// event sources, no InfrastructureProvider CR to hang them off.
func runBootstrapReconciler(ctx context.Context, providerCfg, runtimeCfg *rest.Config, providerKubeconfig []byte) {
	// Buffered depth 1: a burst of events while a pass is running collapses
	// into exactly one follow-up pass, which is all a level-driven reconcile
	// needs.
	trigger := make(chan struct{}, 1)
	poke := func() {
		select {
		case trigger <- struct{}{}:
		default:
		}
	}

	watching := false
	backoff := bootstrapRetryMin
	var retry <-chan time.Time
	poke()
	for {
		select {
		case <-ctx.Done():
			return
		case <-trigger:
		case <-retry:
		}
		retry = nil

		if err := bootstrapOnce(ctx, providerCfg, runtimeCfg, providerKubeconfig); err != nil {
			log.Printf("operator: bootstrap reconcile failed (retry in %s): %v", backoff, err)
			retry = time.After(backoff)
			if backoff *= 2; backoff > bootstrapRetryMax {
				backoff = bootstrapRetryMax
			}
			continue
		}
		backoff = bootstrapRetryMin

		if !watching {
			// Registered only after the first success, because the kinds it
			// watches are installed by the pass itself.
			if err := operator.WatchProviderWorkspace(ctx, providerCfg, poke); err != nil {
				log.Printf("operator: provider workspace watch failed (retry in %s): %v", backoff, err)
				retry = time.After(backoff)
				continue
			}
			watching = true
			log.Printf("operator: watching provider workspace for drift")
		}
		log.Printf("operator: bootstrap reconcile OK")
	}
}

// bootstrapOnce runs one idempotent pass of the provider-workspace bootstrap
// (shared with the CRD operator via operator.Bootstrap), then seeds kro on the
// runtime cluster from the provider kubeconfig. This is the env-driven path —
// the CRD operator (`controller` subcommand) does the same steps per CR.
func bootstrapOnce(ctx context.Context, providerCfg, runtimeCfg *rest.Config, providerKubeconfig []byte) error {
	if err := validateLegacyCodingSandboxImage(); err != nil {
		return err
	}
	workspacePath := os.Getenv("INFRASTRUCTURE_WORKSPACE_PATH")
	if err := operator.Bootstrap(ctx, providerCfg, operator.BootstrapOptions{
		WorkspacePath:        workspacePath,
		APIExportName:        apiExportName,
		CatalogEntryFile:     catalogEntryPath(),
		SkipSeedTemplates:    os.Getenv("INFRASTRUCTURE_SKIP_SEED_TEMPLATES") != "",
		CodingSandboxEnabled: codingSandboxEnabled(),
	}); err != nil {
		return err
	}

	// kro runs single-cluster against the runtime cluster (the instance
	// controller bridges kcp → runtime), so no kcp kubeconfig is seeded onto
	// the runtime cluster.
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
	return operator.Run(ctx, cfg, catalogEntryManifest)
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
