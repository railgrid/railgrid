// Copyright 2026 The Railgrid Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package main

// Multicluster controller manager — reconciles the agents provider's
// tenant-authored CRs (Schedule / Connection / Agent / Toolset / Trigger /
// ModelCredential) across EVERY tenant workspace that has bound this provider's APIExport.
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
// Runs are a CR now (agents.railgrid.ai/Run), and have a reconciler of their
// own: it enforces each run's deadline by waking at the instant written on the
// object, and purges the run's Postgres rows when the object is deleted. The
// transcript and tool trace stay in Postgres under the projection carve-out —
// what the object carries is the run's identity, phase and cost.
//
// Enabled together with the background executor (RAILGRID_PROVIDER_KUBECONFIG).
// Without it the provider runs REST/MCP/portal-only.

import (
	"context"
	"fmt"
	"log"
	"os"
	"strconv"

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
	agentsv1alpha1 "github.com/railgrid/provider-agents/apis/v1alpha1"
	"github.com/railgrid/provider-agents/controller/agent"
	"github.com/railgrid/provider-agents/controller/connection"
	"github.com/railgrid/provider-agents/controller/modelcredential"
	runctl "github.com/railgrid/provider-agents/controller/run"
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
	purge, release := agentTeardown(deps)
	if err := (&agent.Reconciler{PurgeData: purge, ReleaseIdentity: release}).SetupWithManager(mgr); err != nil {
		return fmt.Errorf("agent controller: %w", err)
	}
	if err := (&toolset.Reconciler{}).SetupWithManager(mgr); err != nil {
		return fmt.Errorf("toolset controller: %w", err)
	}
	// Model credentials are validated with the provider's own identity rather
	// than a caller's, because that is the identity an unattended run will use:
	// a credential that only the person who typed it can read is exactly the
	// failure this condition exists to catch.
	if err := (&modelcredential.Reconciler{}).SetupWithManager(mgr); err != nil {
		return fmt.Errorf("model credential controller: %w", err)
	}
	if err := newRunReconciler(deps).SetupWithManager(mgr); err != nil {
		return fmt.Errorf("run controller: %w", err)
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

// runExecution is what the Run reconciler needs from the HTTP half of the
// provider: the worker pool that actually executes a claimed run, the store
// behind a purge, and the cluster→tenant mapping both need. Discovered on the
// executor for the same reason storeTeardown is — the controller only knows the
// logical cluster the CR came from.
type runExecution interface {
	DispatchRun(ctx context.Context, clusterID string, object *agentsv1alpha1.Run) error
	RecoverRun(ctx context.Context, clusterID, agentName, runID string) error
	StopRun(ctx context.Context, clusterID, agentName, runID, reason string) error
	PurgeRunData(ctx context.Context, clusterID, agentName, runID string) error
}

// newRunReconciler builds the Run controller, wired to the provider's executor
// when there is one.
//
// Without it the reconciler still runs but claims nothing: a replica that
// cannot reach tenant workspaces must not take work off the queue, because a
// claim it cannot execute is worse than no claim at all — it holds the run for
// a full ClaimGrace before anyone else may try.
func newRunReconciler(deps api.ControllerDeps) *runctl.Reconciler {
	r := &runctl.Reconciler{ProcessID: processID()}
	execution, ok := deps.Submit.(runExecution)
	if !ok {
		log.Printf("controller manager: unattended runs are not executed and run store data is not purged on delete (the executor exposes no run execution access)")
		return r
	}
	r.Dispatch = execution.DispatchRun
	r.Recover = execution.RecoverRun
	r.Stop = execution.StopRun
	r.PurgeData = execution.PurgeRunData
	return r
}

// processID names this process as the owner of a run it claims.
//
// The pod name in Kubernetes, which is stable for the life of the process and
// unique across replicas — exactly the two properties a claim needs. Outside a
// cluster it falls back to the hostname plus the PID, so two dev processes on
// one machine do not claim as each other.
func processID() string {
	if pod := os.Getenv("POD_NAME"); pod != "" {
		return pod
	}
	host, err := os.Hostname()
	if err != nil {
		host = "unknown"
	}
	return host + "-" + strconv.Itoa(os.Getpid())
}

// storeTeardown is the provider-store teardown the Agent reconciler performs
// on delete: the rows DELETE /api/agents/{name} used to remove inline before
// that handler was deleted. It is discovered on the executor rather than being
// a field of ControllerDeps because the store, and the cluster→tenant mapping
// needed to scope the delete, both live behind the HTTP half of the provider;
// the controller only knows the logical cluster the CR came from.
type storeTeardown interface {
	PurgeAgentData(ctx context.Context, clusterID, agentName string) error
	ReleaseAgentIdentity(ctx context.Context, clusterID, agentName string) error
}

// agentTeardown returns what the Agent finalizer does on delete: purge the
// agent's store rows, and revoke the identity it ran unattended work with.
//
// Both nil disables the finalizer outright, which is the right answer for the
// in-memory dev path: a finalizer nothing can clear would make every agent
// undeletable.
func agentTeardown(deps api.ControllerDeps) (
	purge func(ctx context.Context, clusterID, agentName string) error,
	release func(ctx context.Context, clusterID, agentName string) error,
) {
	teardown, ok := deps.Submit.(storeTeardown)
	if !ok {
		log.Printf("controller manager: a deleted agent's store data is not purged and its identity is not revoked (the executor exposes no teardown); transcripts and runs are left in the store")
		return nil, nil
	}
	return teardown.PurgeAgentData, teardown.ReleaseAgentIdentity
}
