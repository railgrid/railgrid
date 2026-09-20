// Copyright 2026 The Railgrid Authors.
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
	"time"

	coordinationv1 "k8s.io/api/coordination/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"
	"k8s.io/klog/v2"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
	ctrlconfig "sigs.k8s.io/controller-runtime/pkg/config"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	"github.com/railgrid/provider-sdk/apiexportprovider"
	"github.com/railgrid/provider-sdk/identityclient"
	"github.com/railgrid/provider-sdk/leaderelection"
	mcmanager "sigs.k8s.io/multicluster-runtime/pkg/manager"
	mcmulticluster "sigs.k8s.io/multicluster-runtime/pkg/multicluster"

	addonctrl "github.com/railgrid/provider-edges/internal/addonctrl"
	edgectrl "github.com/railgrid/provider-edges/internal/edgectrl"
	"github.com/railgrid/provider-edges/internal/scheduler"
	"github.com/railgrid/provider-edges/internal/servicectrl"
	"github.com/railgrid/provider-edges/internal/status"
	sdktunnel "github.com/railgrid/provider-edges/internal/tunnel"
	sdkinstall "github.com/railgrid/provider-sdk/install"
	"github.com/railgrid/provider-sdk/vwhealth"

	edgesv1alpha1 "github.com/railgrid/provider-edges/apis/v1alpha1"
	edgescheme "github.com/railgrid/provider-edges/scheme"
)

// errControllerDisabled is the sentinel main() checks for so it can log +
// continue without the manager when no kubeconfig is in scope.
var errControllerDisabled = errors.New("no kubeconfig available; edge controller manager disabled")

// endpointSliceName is the APIExportEndpointSlice the multicluster provider
// watches. By convention (provider-sdk) the slice name equals the APIExport
// name — see sdkinstall.Bootstrap / EnsureAPIExportEndpointSlice.
const endpointSliceName = apiExportName

// controllerLeaseName gates the reconcilers on a Lease in the provider
// workspace ("default" namespace — kcp serves Leases in every logical
// cluster), so scaling the deployment past one replica keeps every tenant CR
// single-writer. Non-leaders keep serving the tunnel plane, the data plane,
// MCP and the portal.
const controllerLeaseName = "edges-controllers"

// startEdgeControllerManager wires the two halves of the edges control plane.
//
// They are split because they have different cardinality:
//
//   - The TUNNEL'S TENANT-CONFIG RESOLVER runs on EVERY replica. Any replica
//     can terminate an agent tunnel or serve a data-plane request, and both
//     need a cross-workspace *rest.Config for the tenant the request names.
//     The provider's own SA credential is workspace-scoped — re-rooting it at
//     /clusters/<tenant> is rejected by kcp, which is what broke agent
//     join-token registration in production — so the only credential that
//     works is the APIExport virtual workspace. The resolver is therefore a
//     controller-free multicluster manager over the provider's
//     APIExportEndpointSlice: it engages each tenant logical cluster and
//     hands out its config, and it only ever reads.
//
//   - The RECONCILERS run on the LEADER ONLY, under a Lease in the provider
//     workspace, and are rebuilt per term (a stopped controller-runtime
//     manager cannot be restarted) — the same shape as
//     providers/code/controller_manager.go. Every tenant CR therefore has one
//     writer no matter how many replicas run.
//
// The Lease-based tunnel liveness model and the pod-to-pod relay are
// unaffected: they are tunnel-plane state, claimed by whichever replica holds
// the socket, and the lifecycle reconciler (leader) derives
// status.connected / phase / lastHeartbeatTime from those Leases through the
// leader manager's LOCAL cache.
//
// A nil config means "skip both" (healthz-only / dev). ready, when set,
// reports the leader's multicluster provider watch state for the duration of
// each term.
func startEdgeControllerManager(ctx context.Context, config *rest.Config, tsrv *sdktunnel.Server, hubExternalURL string, hubCAData []byte, devMode bool, identities *identityclient.Client, ready *vwhealth.Readiness) error {
	if config == nil {
		return errControllerDisabled
	}

	ctrl.SetLogger(klog.NewKlogr())

	// The hub provisioner does not create the APIExportEndpointSlice for the
	// provider's APIExport, so ensure it here (idempotent) before building
	// either manager. Best-effort: log + continue; nothing engages until the
	// slice lands.
	dynCl, err := dynamic.NewForConfig(config)
	if err != nil {
		return fmt.Errorf("dynamic client: %w", err)
	}
	// Empty path: the slice lands in whatever workspace `config` addresses,
	// which is also where the APIExport is; the SDK resolves that workspace's
	// canonical path and writes it explicitly (a path-less slice is missed by
	// kcp's per-shard endpoint publisher). Hardcoding the platform path here
	// would make this provider unable to run as an org's self-hosted copy.
	if err := sdkinstall.EnsureAPIExportEndpointSlice(ctx, dynCl, endpointSliceName, apiExportName, ""); err != nil {
		log.Printf("edge controller manager: WARNING could not ensure APIExportEndpointSlice: %v", err)
	}

	if err := startTenantConfigResolver(ctx, config, tsrv); err != nil {
		return fmt.Errorf("tunnel tenant-config resolver: %w", err)
	}

	go func() {
		if err := leaderelection.Run(ctx, leaderelection.Options{
			Config:    config,
			Namespace: leaderelection.DefaultNamespace,
			Name:      controllerLeaseName,
		}, func(termCtx context.Context) {
			if err := runEdgeControllerManager(termCtx, config, tsrv, hubExternalURL, hubCAData, devMode, identities, ready); err != nil {
				log.Printf("edge controller manager exited: %v", err)
			}
		}); err != nil {
			log.Printf("edge controller leader election failed; reconcilers are not running: %v", err)
		}
	}()
	return nil
}

// startTenantConfigResolver builds the replica-local, controller-free
// multicluster manager the tunnel plane resolves tenant configs through, and
// wires it into the tunnel Server.
//
// It registers no reconcilers on purpose: its whole job is to keep the
// APIExportEndpointSlice-backed engagement map warm so mgr.GetCluster answers
// for any workspace that has bound this provider's APIExport. That is a read,
// so running it active-active on N replicas is correct — which is exactly why
// it can be split away from the reconcilers, which cannot.
func startTenantConfigResolver(ctx context.Context, config *rest.Config, tsrv *sdktunnel.Server) error {
	provider, err := apiexportprovider.New(config, endpointSliceName, apiexportprovider.Options{Scheme: edgescheme.NewScheme()})
	if err != nil {
		return fmt.Errorf("creating apiexport multicluster provider: %w", err)
	}
	mgr, err := mcmanager.New(config, provider, manager.Options{
		Scheme:  edgescheme.NewScheme(),
		Metrics: metricsserver.Options{BindAddress: "0"}, // provider serves its own HTTP
		// Nothing in this manager watches the provider workspace, so keep its
		// local cache from starting informers for types it will never read.
		Cache: cache.Options{ByObject: map[client.Object]cache.ByObject{}},
	})
	if err != nil {
		return fmt.Errorf("creating resolver manager: %w", err)
	}

	tsrv.SetTenantConfigGetter(func(ctx context.Context, clusterName string) (*rest.Config, error) {
		cl, err := mgr.GetCluster(ctx, mcmulticluster.ClusterName(clusterName))
		if err != nil {
			return nil, fmt.Errorf("engaging tenant cluster %q: %w", clusterName, err)
		}
		return cl.GetConfig(), nil
	})

	go func() {
		log.Printf("edges tunnel tenant-config resolver starting (endpointSlice=%s)", endpointSliceName)
		if err := mgr.Start(ctx); err != nil {
			log.Printf("edges tunnel tenant-config resolver exited: %v", err)
		}
	}()
	return nil
}

// runEdgeControllerManager builds the leader's multicluster manager, registers
// every reconciler on it and blocks in Start until the leadership term ends.
// Called once per term.
//
// The multicluster provider is attached to readiness for the term: from the
// moment this replica is leader until it stops being one, /readyz (and so the
// hub's BackendHealthy and the heartbeat) says whether tenant workspaces are
// actually being watched. Without that, a watcher that failed to start left
// every signal green while no edge ever got a status.
//
// Edge connectivity status has ONE writer. The tunnel plane records liveness
// only in the registry Leases (provider workspace, label
// edges.railgrid.ai/tunnel-registry: claimed on tunnel open, renewed every 30s
// by the ConnManager sweeper, released on close, expired after
// tunnel.RegistryLeaseTTL otherwise). The lifecycle reconciler watches those
// Leases through this manager's LOCAL cache (the manager's own config
// addresses the provider workspace; the multicluster provider engages only
// tenant clusters, so Leases need this second, single-cluster source) and is
// the sole writer of status.connected / status.phase / status.lastHeartbeatTime
// / Registered=True. connManager only nudges it on local connect/disconnect.
func runEdgeControllerManager(ctx context.Context, config *rest.Config, tsrv *sdktunnel.Server, hubExternalURL string, hubCAData []byte, devMode bool, identities *identityclient.Client, ready *vwhealth.Readiness) error {
	connManager := tsrv.ConnManager()
	s := edgescheme.NewScheme()

	provider, err := apiexportprovider.New(config, endpointSliceName, apiexportprovider.Options{Scheme: s})
	if err != nil {
		return fmt.Errorf("creating apiexport multicluster provider: %w", err)
	}
	if ready != nil {
		defer ready.Attach("controllers", provider)()
	}

	skipNameValidation := true
	mgr, err := mcmanager.New(config, provider, manager.Options{
		Scheme:  s,
		Metrics: metricsserver.Options{BindAddress: "0"}, // provider serves its own HTTP
		// Controller names register process-globally; the manager built for a
		// later leadership term must skip that check.
		Controller: ctrlconfig.Controller{SkipNameValidation: &skipNameValidation},
		Cache: cache.Options{
			// The local cache is only used for the tunnel registry Leases the
			// lifecycle reconcilers watch; restrict the Lease informer to them
			// so presence leases and the leader-election lease stay out of it.
			ByObject: map[client.Object]cache.ByObject{
				&coordinationv1.Lease{}: {
					Label: labels.SelectorFromSet(labels.Set{sdktunnel.TunnelLeaseLabel: "true"}),
				},
			},
		},
	})
	if err != nil {
		return fmt.Errorf("creating multicluster manager: %w", err)
	}

	opts := edgectrl.Options{Identities: identities, HubExternalURL: hubExternalURL, HubCAData: hubCAData, DevMode: devMode}
	// Drive the UpgradeAvailable condition off the hub's /version endpoint. A
	// single cache is shared across both kinds' version reconcilers so many edges
	// cost one periodic hub lookup, not one per edge. Skipped without a hub URL
	// (dev/healthz-only), leaving the condition untouched.
	if hubExternalURL != "" {
		opts.LatestAgentVersion = edgectrl.NewHubVersionCache(hubExternalURL, hubCAData, 10*time.Minute).Get
	}
	// One set of token/RBAC/lifecycle controllers per kind, on the shared
	// multicluster manager. All kinds share the single tunnel ConnManager and
	// Lease registry (keyed by resource/cluster/name); each kind's lifecycle
	// reconciler filters the shared Lease/notification streams to its own
	// resource.
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
	if err := edgectrl.SetupControllers(mgr,
		edgesv1alpha1.MacOSServerGVR, "MacOSServer", edgesv1alpha1.NewMacOSServer,
		connManager, opts,
	); err != nil {
		return fmt.Errorf("MacOSServer controllers: %w", err)
	}

	// Workload scheduling (KubernetesCluster edges only): the scheduler fans a
	// Workload out into one Placement per matching edge; the status
	// aggregator rolls per-edge Placement statuses back up. Each edge's agent
	// applies the derived Deployment locally and reports Placement status.
	if err := scheduler.SetupWithManager(mgr); err != nil {
		return fmt.Errorf("workload scheduler: %w", err)
	}
	if err := status.SetupWithManager(mgr); err != nil {
		return fmt.Errorf("workload status aggregator: %w", err)
	}

	// EdgeService controllers (LinuxServer and MacOSServer edges): the discovery
	// reconciler pulls host services from each connected agent and materializes an
	// EdgeService per service; the validation reconciler checks configured
	// credentials against the service and stamps status. Both share the tunnel
	// ConnManager for agent dials.
	if err := servicectrl.SetupWithManager(mgr, connManager, servicectrl.Options{
		EdgeProxyPublicPath: edgeProxyPublicPath,
	}); err != nil {
		return fmt.Errorf("EdgeService controllers: %w", err)
	}

	// Add-on publisher (host edges): for a runner Addon whose AGENT has
	// reported Allowed=True and published the add-on's token Secret, derive the
	// Service through which the add-on is reachable. It deliberately publishes
	// nothing from hub-side intent alone — see internal/addonctrl and
	// docs/edge-addons.md.
	if err := addonctrl.SetupWithManager(mgr); err != nil {
		return fmt.Errorf("addon controller: %w", err)
	}

	log.Printf("edges controller manager starting (leader, endpointSlice=%s)", endpointSliceName)
	if err := mgr.Start(ctx); err != nil {
		return fmt.Errorf("manager: %w", err)
	}
	return nil
}
