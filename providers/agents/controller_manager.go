// Copyright 2026 The Railgrid Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package main

// Multicluster controller manager — reconciles the agents provider's
// tenant-authored CRs (Schedule / Connection / Agent / Toolset / Trigger)
// across EVERY tenant workspace that has bound this provider's APIExport.
//
// The CRs live in tenant workspaces, so we use the kcp apiexport multicluster
// provider (provider-sdk/apiexportprovider): it watches the provider's
// APIExportEndpointSlice — one virtual-workspace URL per kcp shard — and
// engages each tenant logical cluster it finds behind them. Each reconciler
// resolves a per-tenant client from req.ClusterName.
//
// This replaced a timer that listed every Schedule, Connection and Agent in
// every tenant workspace on every tick. A reconcile now runs when the object
// changes or when the reconciler asked to be woken (a schedule's next fire, a
// token's expiry), which is both cheaper and prompter.
//
// Runs are NOT a CR and have no reconciler: they are Postgres rows, by design
// (see docs/agents-provider-architecture.md). The Schedule reconciler creates
// them through the executor; the recovery sweep in api/recover.go tends them.
//
// Enabled together with the background executor (RAILGRID_PROVIDER_KUBECONFIG).
// Without it the provider runs REST/MCP/portal-only.

import (
	"context"
	"fmt"
	"log"
	"os"

	"k8s.io/client-go/dynamic"
	"k8s.io/klog/v2"
	ctrl "sigs.k8s.io/controller-runtime"
	ctrlconfig "sigs.k8s.io/controller-runtime/pkg/config"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	mcmanager "sigs.k8s.io/multicluster-runtime/pkg/manager"

	"github.com/railgrid/provider-sdk/apiexportprovider"
	sdkinstall "github.com/railgrid/provider-sdk/install"
	"github.com/railgrid/provider-sdk/leaderelection"
	"github.com/railgrid/provider-sdk/vwhealth"

	"github.com/railgrid/provider-agents/api"
	"github.com/railgrid/provider-agents/controller/agent"
	"github.com/railgrid/provider-agents/controller/connection"
	"github.com/railgrid/provider-agents/controller/schedule"
	"github.com/railgrid/provider-agents/controller/toolset"
	"github.com/railgrid/provider-agents/controller/trigger"
	"github.com/railgrid/provider-agents/internal/webhookpath"
	agentsscheme "github.com/railgrid/provider-agents/scheme"
)

// endpointSliceName is the APIExportEndpointSlice the multicluster provider
// watches to discover tenant workspaces. `init` creates it with the export's
// name (provider-sdk/install.Bootstrap uses ExportName for both).
const endpointSliceName = apiExportName

// controllerLeaseName gates the reconcilers on a Lease in the provider
// workspace ("default" namespace — kcp serves Leases in every logical
// cluster), so scaling the deployment past one replica keeps every CR
// single-writer and every Discord bot single-socket. Non-leaders keep serving
// REST/MCP/portal and executing the jobs handed to them.
const controllerLeaseName = "agents-controllers"

// startControllerManager ensures the APIExportEndpointSlice, then campaigns
// for the controller lease and — while leader — runs the multicluster manager
// with the reconcilers, acting through deps (the executor, the Telegram and
// OAuth clients, the Discord gateway) the HTTP half of the provider owns.
// ready, when set, reports the multicluster provider's watch state for the
// duration of each term.
func startControllerManager(ctx context.Context, deps api.ControllerDeps, ready *vwhealth.Readiness) {
	ctrl.SetLogger(klog.NewKlogr())

	// `init` creates the slice, but a provider whose init predates the slice
	// (or whose slice was removed) would otherwise watch nothing, silently.
	// Idempotent and best effort: serve still offers everything else, and the
	// manager engages clusters the moment the slice lands.
	if dyn, err := dynamic.NewForConfig(deps.Config); err != nil {
		log.Printf("controller manager: WARNING building client to ensure APIExportEndpointSlice: %v", err)
	} else if err := sdkinstall.EnsureAPIExportEndpointSlice(ctx, dyn, endpointSliceName, apiExportName, os.Getenv("AGENTS_WORKSPACE_PATH")); err != nil {
		log.Printf("controller manager: WARNING could not ensure APIExportEndpointSlice: %v", err)
	}

	go func() {
		if err := leaderelection.Run(ctx, leaderelection.Options{
			Config:    deps.Config,
			Namespace: leaderelection.DefaultNamespace,
			Name:      controllerLeaseName,
		}, func(termCtx context.Context) {
			if err := runControllerManager(termCtx, deps, ready); err != nil {
				log.Printf("controller manager exited: %v", err)
			}
		}); err != nil {
			log.Printf("controller leader election failed; controllers are not running: %v", err)
		}
	}()
}

// runControllerManager builds the multicluster manager and blocks in Start
// until the leadership term ends. Called once per term — a stopped
// controller-runtime manager cannot be restarted.
//
// The multicluster provider is attached to readiness for the term: from the
// moment this replica is leader until it stops being one, /readyz says whether
// tenant workspaces are actually being watched. Without that, a watcher that
// failed to start would leave every signal green while no schedule ever fired.
func runControllerManager(ctx context.Context, deps api.ControllerDeps, ready *vwhealth.Readiness) error {
	scheme := agentsscheme.NewScheme()

	provider, err := apiexportprovider.New(deps.Config, endpointSliceName, apiexportprovider.Options{Scheme: scheme})
	if err != nil {
		return fmt.Errorf("creating apiexport multicluster provider: %w", err)
	}
	if ready != nil {
		defer ready.Attach("controllers", provider)()
	}
	// The Discord sessions are this term's: the next leader opens its own, and
	// two leaders' worth of sockets would answer every message twice.
	if deps.Gateway != nil {
		defer deps.Gateway.CloseAll()
	}

	skipNameValidation := true
	mgr, err := mcmanager.New(deps.Config, provider, manager.Options{
		Scheme:  scheme,
		Metrics: metricsserver.Options{BindAddress: "0"}, // provider serves its own HTTP; disable controller-runtime metrics
		// Controller names register process-globally; the manager built for a
		// later leadership term must skip that check.
		Controller: ctrlconfig.Controller{SkipNameValidation: &skipNameValidation},
	})
	if err != nil {
		return fmt.Errorf("creating multicluster manager: %w", err)
	}

	if err := (&schedule.Reconciler{Submit: deps.Submit}).SetupWithManager(mgr); err != nil {
		return fmt.Errorf("schedule controller: %w", err)
	}
	if err := (&connection.Reconciler{Telegram: deps.Telegram, OAuth: deps.OAuth, Gateway: deps.Gateway}).SetupWithManager(mgr); err != nil {
		return fmt.Errorf("connection controller: %w", err)
	}
	if err := (&agent.Reconciler{PurgeData: agentDataPurger(deps)}).SetupWithManager(mgr); err != nil {
		return fmt.Errorf("agent controller: %w", err)
	}
	if err := (&toolset.Reconciler{}).SetupWithManager(mgr); err != nil {
		return fmt.Errorf("toolset controller: %w", err)
	}
	// The Trigger reconciler mints inbound webhook URLs, so it needs the same
	// signing key the HTTP layer signs with. It is read from the environment
	// rather than passed through ControllerDeps because the key is
	// configuration, not a dependency: both halves of the provider derive it
	// from the same two variables main.go reads, and internal/webhookpath's
	// golden vectors keep the derivation itself honest. An empty key is not
	// fatal — the reconciler then leaves existing paths alone instead of
	// minting or revoking anything.
	webhookKey := webhookpath.Key(os.Getenv("AGENTS_WEBHOOK_KEY"), os.Getenv("RAILGRID_PROVIDER_KUBECONFIG"))
	if len(webhookKey) == 0 {
		log.Printf("controller manager: WARNING no webhook signing key (AGENTS_WEBHOOK_KEY / RAILGRID_PROVIDER_KUBECONFIG); triggers will not be given inbound URLs")
	}
	if err := (&trigger.Reconciler{WebhookKey: webhookKey}).SetupWithManager(mgr); err != nil {
		return fmt.Errorf("trigger controller: %w", err)
	}

	log.Printf("agents controller manager starting (endpointSlice=%s)", endpointSliceName)
	return mgr.Start(ctx)
}

// storeTeardown is the provider-store teardown the Agent reconciler performs
// on delete: the rows DELETE /api/agents/{name} used to remove inline before
// that handler was deleted. It is discovered on the executor rather than being
// a field of ControllerDeps because the store, and the cluster→tenant mapping
// needed to scope the delete, both live behind the HTTP half of the provider;
// the controller only knows the logical cluster the CR came from.
type storeTeardown interface {
	PurgeAgentData(ctx context.Context, clusterID, agentName string) error
}

// agentDataPurger returns the purge function, or nil when the provider has no
// store to purge from. nil disables the Agent finalizer outright, which is the
// right answer for the in-memory dev path: a finalizer nothing can clear would
// make every agent undeletable.
func agentDataPurger(deps api.ControllerDeps) func(context.Context, string, string) error {
	if p, ok := deps.Submit.(storeTeardown); ok {
		return p.PurgeAgentData
	}
	log.Printf("controller manager: agent store data is not purged on delete (the executor exposes no PurgeAgentData); transcripts and runs of a deleted agent are left in the store")
	return nil
}
