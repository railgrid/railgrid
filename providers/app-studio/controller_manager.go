/*
Copyright 2026 The Railgrid Authors.

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
// mirrors their status back. OPT-IN via RAILGRID_PROVIDER_KUBECONFIG — without
// it the provider runs REST/portal-only.

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	"k8s.io/client-go/rest"
	"k8s.io/klog/v2"
	ctrl "sigs.k8s.io/controller-runtime"
	ctrlconfig "sigs.k8s.io/controller-runtime/pkg/config"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	"github.com/railgrid/provider-sdk/apiexportprovider"
	"github.com/railgrid/provider-sdk/dataplane"
	"github.com/railgrid/provider-sdk/identityclient"
	"github.com/railgrid/provider-sdk/leaderelection"
	"github.com/railgrid/provider-sdk/vwhealth"
	mcmanager "sigs.k8s.io/multicluster-runtime/pkg/manager"

	"github.com/railgrid/provider-app-studio/api"
	"github.com/railgrid/provider-app-studio/bindings"
	"github.com/railgrid/provider-app-studio/controller/project"
	"github.com/railgrid/provider-app-studio/controller/session"
	"github.com/railgrid/provider-app-studio/controller/studio"
	"github.com/railgrid/provider-app-studio/controller/tenantwatch"
	"github.com/railgrid/provider-app-studio/internal/codecommit"
	"github.com/railgrid/provider-app-studio/internal/reconcilesignal"
	"github.com/railgrid/provider-app-studio/internal/scopedidentity"
	appscheme "github.com/railgrid/provider-app-studio/scheme"
	"github.com/railgrid/provider-app-studio/store"
	"github.com/railgrid/provider-app-studio/workspace"
)

// endpointSliceName matches the provider's APIExport name by convention
// (sdkinstall.Bootstrap creates the slice under the same name at init).
const endpointSliceName = apiExportName

// errControllerDisabled is the sentinel runServe checks so it can log +
// continue without the manager when no kubeconfig is in scope.
var errControllerDisabled = errors.New("no kubeconfig available; controller manager disabled")

// controllerLeaseName gates the reconcilers on a Lease in the provider
// workspace ("default" namespace — kcp serves Leases in every logical
// cluster), so scaling the deployment past one replica keeps every Project,
// Studio and Session single-writer. Non-leaders keep serving the REST API,
// the assistant supervisor and the replica-affinity forwarder.
const controllerLeaseName = "app-studio-controllers"

type controllerMode string

const (
	controllerModeRESTOnly controllerMode = "rest-only"
	controllerModeRequired controllerMode = "required"
)

// checkerFunc adapts a plain function to vwhealth.Checker so main can hold
// readiness false for a required controller whose kubeconfig never resolved —
// the one failure the virtual-workspace probe cannot see, because without a
// config there is nothing to probe.
type checkerFunc func() error

func (f checkerFunc) Check() error { return f() }

// controllerDeps carries the runtime collaborators the Project reconciler
// shares with the HTTP layer: the on-disk workspace store (commit
// convergence reads it), the assistant-busy gate, the hub address for
// MCP commit calls, and the signal buses the HTTP layer publishes thread,
// turn, and workspace transitions on.
type controllerDeps struct {
	Actions     bindings.ActionsRuntimeConfig
	Workspace   *workspace.FileStore
	Busy        func(workspace.Scope) bool
	Owns        func(workspace.Scope) bool
	OnCommitted func(context.Context, workspace.Scope, project.CommitResult)
	Store       store.Store
	// StopAssistant interrupts an assistant run for a project being deleted.
	// The Project finalizer calls it before purging anything the turn might
	// still be writing to.
	StopAssistant func(context.Context, workspace.Scope) error
	HubBase       string
	HubInsecure   bool
	// Callers acts as the provider through its export virtual workspace
	// (dataplane.Callers with WithProviderConfig); it is how the dependency
	// watch lists and watches the composed kinds under the claims the tenant
	// accepted, and how the Project reconciler calls the Code provider's
	// commit verbs it has claimed. Nil (REST-only dev) means no watches and
	// deferred commits.
	Callers *dataplane.Callers
	// SessionSignals / ProjectSignals wake the Session and Project
	// reconcilers on assistant and workspace transitions (nil: resync only).
	SessionSignals *reconcilesignal.Bus
	ProjectSignals *reconcilesignal.Bus
	// SessionRetention is how long a conversation survives its last activity
	// before the Session reconciler deletes it (and its finalizer purges the
	// store). Zero keeps conversations indefinitely.
	SessionRetention time.Duration
}

// projectCallers hands the Project reconciler its commit caller, keeping a
// nil *dataplane.Callers a nil interface (a typed nil would pass the
// reconciler's "no credential" check and then panic).
func projectCallers(callers *dataplane.Callers) codecommit.Caller {
	if callers == nil {
		return nil
	}
	return callers
}

// dependencyWatches builds the per-workspace watch hub the Project and Studio
// reconcilers share. The watch lists and watches the composed kinds THROUGH
// App Studio's own export virtual workspace, as the provider: the composition
// is a permission claim the tenant accepted, and kcp serves a claimed kind on
// the claimer's virtual workspace. No per-workspace identity is minted for it.
// Without a provider credential (REST-only dev) there are no watches.
func dependencyWatches(deps controllerDeps) *tenantwatch.Hub {
	if deps.Callers == nil {
		return nil
	}
	return tenantwatch.NewHub(deps.Callers.AsProvider)
}

// scopedIdentities builds the hub identity client the Project and Studio
// reconcilers mint each owner's identity with. It is what a project presents
// to things that are NOT the Kubernetes API: the Code provider's commit and
// stage-commit-bundle actions, the workspace's MCP aggregate, and a data-plane
// verb on its own instance. It is also what the per-workspace dependency watch
// lists and watches with (dependencyWatches below).
//
// It is NOT how the dependency objects are converged any more. App Studio's
// APIExport claims instances, repositories and repositorycommits with no
// identityHash, kcp resolves those claims per consumer workspace, and the
// reconcilers read and write them on the manager's own client — the same one
// that carries this provider's own kinds.
//
// A failure here is logged and degrades to nil rather than refusing to start:
// a REST-only dev deployment has no hub to ask, and the Project and Studio
// CRs still converge without one.
func scopedIdentities(deps controllerDeps) *scopedidentity.Cache {
	if deps.HubBase == "" {
		return nil
	}
	insecure := deps.HubInsecure
	client, err := identityclient.New(identityclient.Options{
		HubURL:   deps.HubBase,
		Provider: providerName,
		Insecure: &insecure,
	})
	if err != nil {
		log.Printf("WARNING app-studio: the hub identity service is unavailable, so per-project identities cannot be minted: %v", err)
		return nil
	}
	return scopedidentity.New(client)
}

// projectCommitNotifier adapts the API server's commit notification to the
// Project reconciler's OnCommitted hook, keeping the api package free of
// controller types.
func projectCommitNotifier(notify func(context.Context, workspace.Scope, api.ProjectCommit)) func(context.Context, workspace.Scope, project.CommitResult) {
	return func(ctx context.Context, scope workspace.Scope, commit project.CommitResult) {
		notify(ctx, scope, api.ProjectCommit{
			RepositoryRef: commit.RepositoryRef,
			CommitSHA:     commit.CommitSHA,
			CommitURL:     commit.CommitURL,
			Branch:        commit.Branch,
			Files:         commit.Files,
		})
	}
}

// startControllerManager campaigns for the controller lease and — while this
// replica leads — runs the multicluster manager with the Project, Session and
// Studio reconcilers. A nil config means "skip the manager, run REST-only".
//
// It returns as soon as the campaign is under way: the API server, the
// assistant supervisor and the replica-affinity forwarder must keep serving on
// every replica, leader or not, so nothing here may block them. ready, when
// set, carries the multicluster provider's watch state for the duration of
// each term.
func startControllerManager(ctx context.Context, config *rest.Config, deps controllerDeps, ready *vwhealth.Readiness) error {
	if config == nil {
		return errControllerDisabled
	}

	ctrl.SetLogger(klog.NewKlogr())

	go func() {
		if err := leaderelection.Run(ctx, leaderelection.Options{
			Config:    config,
			Namespace: leaderelection.DefaultNamespace,
			Name:      controllerLeaseName,
		}, func(termCtx context.Context) {
			if err := runControllerManager(termCtx, config, deps, ready); err != nil {
				log.Printf("controller manager exited: %v", err)
			}
		}); err != nil {
			log.Printf("controller leader election failed; controllers are not running: %v", err)
		}
	}()
	return nil
}

// runControllerManager builds the multicluster manager and blocks in Start
// until the leadership term ends. Called once per term — a stopped
// controller-runtime manager cannot be restarted, and neither can the tenant
// watch hub it shares with the Project and Studio reconcilers, so both are
// built fresh here and die with the term.
//
// The multicluster provider is attached to readiness for the term: from the
// moment this replica is leader until it stops being one, /readyz (and so the
// hub's BackendHealthy) and the heartbeat say whether tenant workspaces are
// actually being watched.
func runControllerManager(ctx context.Context, config *rest.Config, deps controllerDeps, ready *vwhealth.Readiness) error {
	scheme := appscheme.NewScheme()

	provider, err := apiexportprovider.New(config, endpointSliceName, apiexportprovider.Options{Scheme: scheme})
	if err != nil {
		return fmt.Errorf("creating apiexport multicluster provider: %w", err)
	}
	if ready != nil {
		defer ready.Attach("controllers", provider)()
	}

	skipNameValidation := true
	mgr, err := mcmanager.New(config, provider, manager.Options{
		Scheme:  scheme,
		Metrics: metricsserver.Options{BindAddress: "0"}, // provider serves its own HTTP; disable controller-runtime metrics
		// Controller names register process-globally; the manager built for a
		// later leadership term must skip that check.
		Controller: ctrlconfig.Controller{SkipNameValidation: &skipNameValidation},
	})
	if err != nil {
		return fmt.Errorf("creating multicluster manager: %w", err)
	}
	attachments, ok := deps.Store.(store.AttachmentStore)
	if !ok {
		return fmt.Errorf("project controller: message store does not support attachment lifecycle cleanup")
	}

	identities := scopedIdentities(deps)
	watches := dependencyWatches(deps)
	if err := (&project.Reconciler{
		Actions:       deps.Actions,
		Workspace:     deps.Workspace,
		Busy:          deps.Busy,
		Owns:          deps.Owns,
		OnCommitted:   deps.OnCommitted,
		Attachments:   attachments,
		Store:         deps.Store,
		StopAssistant: deps.StopAssistant,
		HubBase:       deps.HubBase,
		HubInsecure:   deps.HubInsecure,
		Callers:       projectCallers(deps.Callers),
		Watches:       watches,
		Signals:       deps.ProjectSignals,
		Identities:    identities,
	}).SetupWithManager(mgr); err != nil {
		return fmt.Errorf("project controller: %w", err)
	}
	if err := (&session.Reconciler{Store: deps.Store, Signals: deps.SessionSignals, Retention: deps.SessionRetention}).SetupWithManager(mgr); err != nil {
		return fmt.Errorf("session controller: %w", err)
	}
	if err := (&studio.Reconciler{
		Watches: watches,
	}).SetupWithManager(mgr); err != nil {
		return fmt.Errorf("studio controller: %w", err)
	}

	log.Printf("app-studio controller manager starting (endpointSlice=%s)", endpointSliceName)
	return mgr.Start(ctx)
}

// controllerModeFromEnv makes REST-only operation an intentional local-dev
// choice while keeping a configured provider kubeconfig controller-required
// by default. The chart sets required explicitly so a production pod cannot
// accidentally become ready as a REST-only process.
func controllerModeFromEnv() controllerMode {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("APP_STUDIO_CONTROLLER_MODE"))) {
	case string(controllerModeRESTOnly), "rest_only", "rest":
		return controllerModeRESTOnly
	case string(controllerModeRequired), "controller":
		return controllerModeRequired
	case "":
		if strings.EqualFold(strings.TrimSpace(os.Getenv("APP_STUDIO_REST_ONLY")), "true") {
			return controllerModeRESTOnly
		}
		if strings.TrimSpace(os.Getenv("RAILGRID_PROVIDER_KUBECONFIG")) != "" ||
			strings.TrimSpace(os.Getenv("KUBECONFIG")) != "" {
			return controllerModeRequired
		}
		return controllerModeRESTOnly
	default:
		// Fail closed for an invalid mode: a deployment with a typo must not
		// advertise a REST-only health contract while its controller is absent.
		log.Printf("unknown APP_STUDIO_CONTROLLER_MODE=%q; requiring controller", os.Getenv("APP_STUDIO_CONTROLLER_MODE"))
		return controllerModeRequired
	}
}
