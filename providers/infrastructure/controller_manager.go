// Copyright 2026 The Railgrid Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package main

// The provider's write loops and the credential they run with.
//
// serve NEVER bootstraps and never runs with an admin credential. The
// high-privilege install chain (CRDs, CachedResource, EndpointSlice, APIExport
// schemas) belongs to `init` and to the operator; serve is handed the
// workspace-scoped kubeconfig those produced and does nothing it cannot do with
// it. There is exactly one env var for that kubeconfig —
// RAILGRID_PROVIDER_KUBECONFIG, the name every chart sets on the serve
// container and every other provider reads — and no fallback: not the pod's
// ServiceAccount (that points at the HOST cluster, not kcp), not a root-scoped
// admin kubeconfig retargeted at a workspace. Missing it is a startup failure,
// not a degraded mode.
//
// One lease, one manager. The Instance controller's multicluster manager owns
// the provider workspace as its local cluster, so the Template controller runs
// on mgr.GetLocalManager() rather than on a manager (and lease) of its own.

import (
	"context"
	"fmt"
	"log"
	"os"

	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/klog/v2"
	ctrl "sigs.k8s.io/controller-runtime"
	ctrlconfig "sigs.k8s.io/controller-runtime/pkg/config"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	"github.com/railgrid/provider-sdk/leaderelection"

	"github.com/railgrid/provider-infrastructure/backend"
	krobackend "github.com/railgrid/provider-infrastructure/backend/kro"
	"github.com/railgrid/provider-infrastructure/backend/stub"
	"github.com/railgrid/provider-infrastructure/controller/instance"
	"github.com/railgrid/provider-infrastructure/controller/template"
	"github.com/railgrid/provider-infrastructure/install"
	"github.com/railgrid/provider-infrastructure/networkpolicy"
)

// Leases gating this binary's singleton write loops, held in the provider
// workspace ("default" namespace — kcp serves Leases in every logical
// cluster). REST/MCP/portal serving is untouched — non-leaders keep serving.
const (
	// controllerLeaseName gates the single write loop: the Instance
	// controller's multicluster manager, with the Template controller folded
	// onto its local (provider-workspace) manager. It keeps the Instance
	// lease's name because the Instance reconciler is the one that owns
	// cross-cluster state, so a rolling update must never run two of them.
	controllerLeaseName = "infrastructure-instance"
	// bootstrapLeaseName gates the operator's bootstrap reconciler.
	bootstrapLeaseName = "infrastructure-bootstrap"
)

// providerKubeconfigEnv is the one and only kubeconfig serve reads: the
// workspace-scoped credential `init` mints (or the operator replicates), which
// is what every chart already sets on the serve container.
const providerKubeconfigEnv = "RAILGRID_PROVIDER_KUBECONFIG"

// loadControllerConfig returns the rest.Config every serve-side controller, the
// tenant client factory and both readiness probes run against. It resolves
// exactly one source — see providerKubeconfigEnv — and fails otherwise, because
// each alternative is a way to run serve with credentials it must not have:
// an admin kubeconfig (retargeted or not) hands serve rights `init` deliberately
// did not grant it, and the in-cluster ServiceAccount silently points every kcp
// controller at the host cluster.
func loadControllerConfig() (*rest.Config, error) {
	path := os.Getenv(providerKubeconfigEnv)
	if path == "" {
		return nil, fmt.Errorf(
			"%s is not set: serve runs only with the workspace-scoped provider kubeconfig that `infrastructure-provider init` mints "+
				"(or that the operator replicates into the serve Secret) — run `init` first, then point %s at it",
			providerKubeconfigEnv, providerKubeconfigEnv)
	}
	cfg, err := clientcmd.BuildConfigFromFlags("", path)
	if err != nil {
		return nil, fmt.Errorf("%s=%s: %w", providerKubeconfigEnv, path, err)
	}
	log.Printf("kcp config resolved from %s (host=%s)", providerKubeconfigEnv, cfg.Host)
	return cfg, nil
}

// startControllers campaigns for the controller lease and, while leader, runs
// the provider's reconcilers against its own kcp workspace. Everything is
// resolved up front so a misconfiguration is reported at startup rather than
// whenever this replica happens to win an election.
//
// With a kro runtime cluster in scope both controllers run: the Instance
// reconciler on the APIExport virtual workspace, and the Template reconciler on
// that manager's local cluster (the provider workspace) with the runtime
// cluster's RGD cache as a second event source. Without one — dev and the e2e
// suite, where templates are reconciled by the stub backend — only the Template
// reconciler runs, on a plain manager over the same workspace, under the same
// lease.
func startControllers(ctx context.Context, config *rest.Config) error {
	if config == nil {
		return fmt.Errorf("startControllers: nil provider config")
	}

	// Register controller-runtime's logger once before building any manager.
	// Without this, the first internal log call (e.g. the priorityqueue depth
	// report) prints a "log.SetLogger(...) was never called" stack trace and
	// swallows all controller-runtime logs.
	ctrl.SetLogger(klog.NewKlogr())

	registry := backend.NewRegistry()
	if err := registry.Register(stub.New()); err != nil {
		return fmt.Errorf("register stub backend: %w", err)
	}

	// The kro backend authors RGDs on the runtime cluster (where the kro
	// controller watches them), NOT in this provider's kcp workspace. It is
	// resolved from KRO_KUBECONFIG, else the pod's in-cluster config (the
	// operator's in-cluster-runtime mode).
	runtimeClient, runtimeCfg, runtimeSrc, runtimeErr := runtimeDynamicClient()
	if runtimeErr == nil {
		if err := registry.Register(krobackend.New(runtimeClient)); err != nil {
			return fmt.Errorf("register kro backend: %w", err)
		}
		log.Printf("controllers: kro backend registered (RGD runtime cluster: %s)", runtimeSrc)
	} else {
		log.Printf("controllers: no kro runtime cluster (%v) — stub backend only, Instance reconciler disabled", runtimeErr)
	}

	// Tenant runtime-namespace ingress isolation (off unless
	// RAILGRID_TENANT_NETWORK_POLICY_ENABLED=true). The exposure Gateway's
	// namespace is always admitted, resolved exactly as the kro backend
	// resolves ${railgrid.gatewayNamespace} so the policy follows the HTTPRoutes.
	gatewayNamespace := os.Getenv("RAILGRID_GATEWAY_NAMESPACE")
	if gatewayNamespace == "" {
		gatewayNamespace = krobackend.DefaultGatewayNamespace
	}
	netpol, err := networkpolicy.FromEnv(gatewayNamespace)
	if err == nil {
		err = netpol.Validate()
	}
	if err != nil {
		return fmt.Errorf("tenant network policy: %w", err)
	}
	log.Printf("controllers: tenant network policy enabled=%t gatewayNamespace=%q allowedNamespaces=%q allowedCIDRs=%q",
		netpol.Enabled, netpol.GatewayNamespace, netpol.AllowedNamespaces, netpol.AllowedCIDRs)

	baseDomain := os.Getenv("RAILGRID_APP_BASE_DOMAIN")

	// Leader-elected: instances own runtime-cluster state (kro CRs, bridged
	// Secrets) and Templates own RGDs, so exactly one replica may reconcile
	// them. The managers are rebuilt fresh each term — a stopped
	// controller-runtime manager cannot be restarted.
	go func() {
		if err := leaderelection.Run(ctx, leaderelection.Options{
			Config:    config,
			Namespace: leaderelection.DefaultNamespace,
			Name:      controllerLeaseName,
		}, func(termCtx context.Context) {
			var runErr error
			if runtimeErr == nil {
				runErr = runControllers(termCtx, config, registry, instance.Config{
					ProviderConfig:       config,
					APIExportName:        install.APIExportName,
					BaseDomain:           baseDomain,
					Runtime:              runtimeClient,
					RuntimeConfig:        runtimeCfg,
					CodingSandboxEnabled: codingSandboxEnabled(),
					NetworkPolicy:        netpol,
				})
			} else {
				runErr = runTemplateOnly(termCtx, config, registry)
			}
			if runErr != nil {
				log.Printf("controllers: exited: %v", runErr)
			}
		}); err != nil {
			log.Printf("controller leader election failed; no reconcilers are running: %v", err)
		}
	}()
	return nil
}

// runControllers builds the Instance controller's multicluster manager, folds
// the Template controller onto its local (provider-workspace) manager, and
// blocks in Start until the leadership term ends. Called once per term.
func runControllers(ctx context.Context, config *rest.Config, registry *backend.Registry, cfg instance.Config) error {
	c, err := instance.New(cfg)
	if err != nil {
		return fmt.Errorf("instance controller: %w", err)
	}
	// The Template controller watches Templates in the provider workspace —
	// the multicluster manager's local cluster — and the RGDs the kro backend
	// authors on the runtime cluster, whose cache the Instance controller
	// already runs. One manager, one cache per cluster, one lease.
	if err := (&template.Reconciler{
		Client:               c.LocalManager().GetClient(),
		Backends:             registry,
		CodingSandboxEnabled: codingSandboxEnabled(),
		RuntimeCache:         c.RuntimeCache(),
	}).SetupWithManager(c.LocalManager()); err != nil {
		return fmt.Errorf("template controller: %w", err)
	}

	log.Printf("controllers: starting (apiExport=%s baseDomain=%q backends=%v)", cfg.APIExportName, cfg.BaseDomain, registry.Names())
	return c.Start(ctx)
}

// runTemplateOnly runs the Template controller alone on a plain manager over
// the provider workspace. This is the no-runtime-cluster path: there is nothing
// for the Instance reconciler to materialize instances on, but Templates still
// validate and reconcile through the stub backend (dev, and the e2e suite).
func runTemplateOnly(ctx context.Context, config *rest.Config, registry *backend.Registry) error {
	skipNameValidation := true
	mgr, err := manager.New(config, manager.Options{
		// The provider's own HTTP server owns the port in dev; a metrics
		// listener here would collide with it.
		Metrics: metricsserver.Options{BindAddress: "0"},
		// Controller names register process-globally; the manager built for a
		// later leadership term must skip that check.
		Controller: ctrlconfig.Controller{SkipNameValidation: &skipNameValidation},
	})
	if err != nil {
		return fmt.Errorf("manager.New: %w", err)
	}
	if err := (&template.Reconciler{
		Client:               mgr.GetClient(),
		Backends:             registry,
		CodingSandboxEnabled: codingSandboxEnabled(),
	}).SetupWithManager(mgr); err != nil {
		return fmt.Errorf("template controller: %w", err)
	}
	log.Printf("controllers: starting Template controller only (backends=%v)", registry.Names())
	return mgr.Start(ctx)
}
