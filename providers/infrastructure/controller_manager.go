// Copyright 2026 The Faros Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package main

// Platform controller manager — the one that reconciles Template CRs
// into backend setup (kro RGDs). Lives alongside the legacy
// REST surface; the two coexist for PRs A-D and the REST handlers get
// deleted in PR E once the UI + MCP have migrated to the kcp-native
// path.
//
// The manager is OPT-IN via INFRASTRUCTURE_CONTROLLER_KUBECONFIG (or
// the standard KUBECONFIG fallback). When neither is set the provider
// runs as it does today: REST broker, no controller. That keeps the
// dev-mode/stub flow intact while the new code lands.

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"

	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/klog/v2"
	ctrl "sigs.k8s.io/controller-runtime"
	ctrlconfig "sigs.k8s.io/controller-runtime/pkg/config"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	"github.com/faroshq/provider-sdk/leaderelection"

	"github.com/faroshq/provider-infrastructure/backend"
	krobackend "github.com/faroshq/provider-infrastructure/backend/kro"
	"github.com/faroshq/provider-infrastructure/backend/stub"
	"github.com/faroshq/provider-infrastructure/controller/template"
	"github.com/faroshq/provider-infrastructure/install"
)

// Leases gating this binary's singleton write loops, all held in the provider
// workspace ("default" namespace — kcp serves Leases in every logical
// cluster). One lease per loop so each is independently singleton; which
// replica holds which does not matter. REST/MCP/portal serving is untouched —
// non-leaders keep serving.
const (
	controllerLeaseName = "infrastructure-controllers"
	instanceLeaseName   = "infrastructure-instance"
	bootstrapLeaseName  = "infrastructure-bootstrap"
)

// startControllerManager installs the platform CRDs (legacy single-binary
// mode), then campaigns for the controller lease and — while leader — runs a
// controller-runtime manager pointed at the provider's own kcp workspace with
// the Template controller on it. The caller loads the kcp config (shared with
// the tenant client) and passes it in; a nil config means "skip the manager,
// run REST-only".
func startControllerManager(ctx context.Context, config *rest.Config) error {
	if config == nil {
		return errControllerDisabled
	}

	// In the init/serve split (INFRASTRUCTURE_KUBECONFIG set), init has
	// already done all the high-privilege bootstrap. Serve runs with a
	// narrow SA that doesn't have all the rights needed to re-apply
	// CachedResources, so we MUST skip these calls. In the legacy
	// single-binary mode we still run them so dev clusters that haven't
	// migrated to init/serve keep working.
	if os.Getenv("INFRASTRUCTURE_KUBECONFIG") == "" {
		if err := install.CRDs(ctx, config); err != nil {
			return fmt.Errorf("install CRDs: %w", err)
		}
		// Legacy single-binary path: CachedResource + EndpointSlice before
		// APIExport so templates use virtual storage. Templates MUST be served
		// via virtual storage (to project into tenant workspaces) — never fall
		// back to CRD storage; fail so a restart retries until the identityHash
		// is ready.
		if err := install.PlatformCachedResources(ctx, config); err != nil {
			return fmt.Errorf("install CachedResources: %w", err)
		}
		if err := install.PlatformCachedResourceEndpointSlices(ctx, config); err != nil {
			return fmt.Errorf("install EndpointSlice: %w", err)
		}
		hash, err := install.WaitForCachedResourceIdentity(ctx, config)
		if err != nil {
			return fmt.Errorf("CachedResource identityHash not ready (templates require virtual storage): %w", err)
		}
		if hash == "" {
			return fmt.Errorf("CachedResource identityHash empty (templates require virtual storage)")
		}
		if err := install.PlatformSchemaInAPIExport(ctx, config, hash); err != nil {
			return fmt.Errorf("register platform schemas on APIExport: %w", err)
		}
	}

	// Register controller-runtime's logger once before building the
	// manager. Without this, the first internal log call (e.g. the
	// priorityqueue depth report) prints a "log.SetLogger(...) was never
	// called" stack trace and swallows all controller-runtime logs.
	ctrl.SetLogger(klog.NewKlogr())

	// Leader-elected: only the replica holding the lease runs the Template
	// controller, so scaling the serve deployment past one replica stops the
	// two-active-managers conflict churn. The manager is rebuilt fresh each
	// term — a stopped controller-runtime manager cannot be restarted.
	go func() {
		if err := leaderelection.Run(ctx, leaderelection.Options{
			Config:    config,
			Namespace: leaderelection.DefaultNamespace,
			Name:      controllerLeaseName,
		}, func(termCtx context.Context) {
			if err := runTemplateControllerManager(termCtx, config); err != nil {
				log.Printf("controller manager exited: %v", err)
			}
		}); err != nil {
			log.Printf("controller leader election failed; Template controller is not running: %v", err)
		}
	}()
	return nil
}

// runTemplateControllerManager builds the Template controller manager and
// blocks in Start until the leadership term ends. Called once per term.
func runTemplateControllerManager(ctx context.Context, config *rest.Config) error {
	skipNameValidation := true
	mgr, err := manager.New(config, manager.Options{
		// Disable the metrics server in PR A; the bind on :8080 would
		// collide with the provider's own HTTP server in dev. PR E
		// adds it back on a configurable port.
		Metrics: metricsserver.Options{BindAddress: "0"},
		// Controller names register process-globally; the manager built for a
		// later leadership term must skip that check.
		Controller: ctrlconfig.Controller{SkipNameValidation: &skipNameValidation},
	})
	if err != nil {
		return fmt.Errorf("manager.New: %w", err)
	}

	registry := backend.NewRegistry()
	if err := registry.Register(stub.New()); err != nil {
		return fmt.Errorf("register stub backend: %w", err)
	}

	// kro backend: authors RGDs on the runtime cluster (where the kro
	// controller watches RGDs — a kind cluster in dev), NOT this provider's
	// kcp workspace. It needs a separate client; KRO_KUBECONFIG points at
	// that cluster (the same kubeconfig the legacy kro broker reads). When
	// unset we run stub-only so dev/REST-only flows still boot.
	// Resolve the kro runtime cluster: explicit KRO_KUBECONFIG, else the pod's
	// in-cluster config (the operator's in-cluster-runtime mode — serve runs in
	// the runtime cluster and authors RGDs against it via its pod SA). Falls
	// back to stub-only when neither is available (dev/REST-only).
	var kroCfg *rest.Config
	var kroSrc string
	if p := os.Getenv("KRO_KUBECONFIG"); p != "" {
		c, err := clientcmd.BuildConfigFromFlags("", p)
		if err != nil {
			return fmt.Errorf("loading KRO_KUBECONFIG for kro backend: %w", err)
		}
		kroCfg, kroSrc = c, "KRO_KUBECONFIG="+p
	} else if c, err := rest.InClusterConfig(); err == nil {
		kroCfg, kroSrc = c, "in-cluster"
	}
	if kroCfg != nil {
		kroDyn, err := dynamic.NewForConfig(kroCfg)
		if err != nil {
			return fmt.Errorf("kro backend dynamic client: %w", err)
		}
		if err := registry.Register(krobackend.New(kroDyn)); err != nil {
			return fmt.Errorf("register kro backend: %w", err)
		}
		log.Printf("controller manager: kro backend registered (RGD runtime cluster: %s)", kroSrc)
	} else {
		log.Printf("controller manager: no kro runtime config (KRO_KUBECONFIG unset, not in a pod) — kro backend not registered (stub-only)")
	}

	if err := (&template.Reconciler{
		Client:   mgr.GetClient(),
		Backends: registry,
	}).SetupWithManager(mgr); err != nil {
		return fmt.Errorf("template controller: %w", err)
	}

	log.Printf("infrastructure controller manager starting (backends=%v)", registry.Names())
	return mgr.Start(ctx)
}

// loadControllerConfig returns a rest.Config for the workspace the
// platform controllers target. Looked up in this order:
//
//	FAROS_PROVIDER_KUBECONFIG             — standardized across all providers
//	INFRASTRUCTURE_KUBECONFIG             — minted SA kubeconfig from `init`
//	INFRASTRUCTURE_CONTROLLER_KUBECONFIG  — legacy provider-specific override
//	KUBECONFIG                            — standard env var
//	in-cluster service account            — when run as a pod
//
// FAROS_PROVIDER_KUBECONFIG is the name every chart sets on the serve
// container, and the name the other eight providers read. Until it was
// honored here, a chart-deployed serve container found none of the
// provider-specific names — only `init` is given INFRASTRUCTURE_KUBECONFIG —
// and fell through to the in-cluster ServiceAccount. That silently pointed
// every kcp controller at the HOST cluster, surfacing as an unrelated-looking
// RBAC error the first time something touched the API (leases in "default").
//
// The minted path wins because serve mode is supposed to run with
// the lowest-privilege identity available. If init has already run,
// INFRASTRUCTURE_KUBECONFIG points at a SA token bound to the
// narrow ClusterRole in install/identity.go. The remaining entries
// stay as escape hatches for dev clusters that haven't migrated to
// the init/serve split.
//
// Returns errControllerDisabled when none of them resolve; the
// caller logs + continues without the controller.
func loadControllerConfig() (*rest.Config, error) {
	c, source, err := loadControllerConfigRaw()
	if err != nil {
		return nil, err
	}
	// Say which source won. Every controller and both leader elections run
	// against this config, so picking the wrong one misdirects the whole
	// provider — and the symptom surfaces far from the cause.
	log.Printf("kcp config resolved from %s (host=%s)", source, c.Host)
	if source == sourceInCluster {
		log.Printf("WARNING: no provider kubeconfig in scope, so controllers will run "+
			"against the HOST cluster, not kcp. Set %s to the mounted provider kubeconfig.",
			"FAROS_PROVIDER_KUBECONFIG")
	}
	// When INFRASTRUCTURE_WORKSPACE_PATH is set, retarget the config host at
	// /clusters/<path>. This lets serve run with a root-scoped (admin)
	// kubeconfig pointed at the provider workspace — so the operator-driven
	// flow no longer needs `init` to mint a workspace-scoped kubeconfig.
	// Idempotent: an already workspace-scoped kubeconfig (prod) is unchanged.
	if ws := os.Getenv("INFRASTRUCTURE_WORKSPACE_PATH"); ws != "" {
		host, herr := retargetHostToWorkspace(c.Host, ws)
		if herr != nil {
			return nil, fmt.Errorf("retarget controller kubeconfig to workspace %q: %w", ws, herr)
		}
		c.Host = host
	}
	return c, nil
}

// sourceInCluster names the last-resort branch of loadControllerConfigRaw.
const sourceInCluster = "the in-cluster ServiceAccount"

// controllerKubeconfigEnvs is the resolution order, most-specific first. The
// standardized FAROS_PROVIDER_KUBECONFIG leads: it is what every chart sets on
// the serve container and what the other providers read.
var controllerKubeconfigEnvs = []string{
	"FAROS_PROVIDER_KUBECONFIG",
	"INFRASTRUCTURE_KUBECONFIG",
	"INFRASTRUCTURE_CONTROLLER_KUBECONFIG",
	"KUBECONFIG",
}

// loadControllerConfigRaw returns the config and the name of the source it
// came from, so the caller can report which one won.
func loadControllerConfigRaw() (*rest.Config, string, error) {
	for _, env := range controllerKubeconfigEnvs {
		p := os.Getenv(env)
		if p == "" {
			continue
		}
		c, err := clientcmd.BuildConfigFromFlags("", p)
		if err != nil {
			return nil, "", fmt.Errorf("%s: %w", env, err)
		}
		return c, env, nil
	}
	// In-cluster fallback. The error returned by InClusterConfig is
	// the right "not running in a pod" signal so we let it surface
	// up the chain as errControllerDisabled.
	c, err := rest.InClusterConfig()
	if err != nil {
		return nil, "", errControllerDisabled
	}
	return c, sourceInCluster, nil
}

// errControllerDisabled is the sentinel main() checks for so it can
// log + continue without the manager when no kubeconfig is in scope.
var errControllerDisabled = errors.New("no kubeconfig available; controller manager disabled")
