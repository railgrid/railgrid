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
	"sigs.k8s.io/controller-runtime/pkg/manager"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	"github.com/railgrid/provider-sdk/apiexportprovider"
	mcmanager "sigs.k8s.io/multicluster-runtime/pkg/manager"
	mcmulticluster "sigs.k8s.io/multicluster-runtime/pkg/multicluster"

	addonctrl "github.com/railgrid/provider-edges/internal/addonctrl"
	edgectrl "github.com/railgrid/provider-edges/internal/edgectrl"
	"github.com/railgrid/provider-edges/internal/events"
	"github.com/railgrid/provider-edges/internal/scheduler"
	"github.com/railgrid/provider-edges/internal/servicectrl"
	"github.com/railgrid/provider-edges/internal/status"
	sdktunnel "github.com/railgrid/provider-edges/internal/tunnel"
	sdkinstall "github.com/railgrid/provider-sdk/install"

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

// eventsMaxAge bounds how long an edge event is retained in the in-memory store
// (on top of the per-service count cap), so the "recent events" a tool returns
// stay recent even for a quiet camera.
const eventsMaxAge = 6 * time.Hour

// startEdgeControllerManager builds the multicluster manager and starts the
// edge token / RBAC / lifecycle reconcilers. A nil config means "skip the
// manager" (healthz-only / dev).
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
//
// Still NOT leader-elected (chart replicaCount is 1 today): this manager
// doubles as the tunnel plane's tenant-config resolver (SetTenantConfigGetter
// below goes through mgr.GetCluster), so it must run on every replica. Because
// the lifecycle reconciler derives status from the shared Leases and writes
// only on a diff, running it active-active on N replicas is correct — the
// writes are duplicated, never contradictory. Leader-electing the reconcilers
// requires first giving the serving path a slice-backed tenant resolver (see
// the databricks SliceAuthority pattern) — tracked in
// docs/provider-horizontal-scaling.md.
func startEdgeControllerManager(ctx context.Context, config *rest.Config, tsrv *sdktunnel.Server, hubExternalURL string, hubCAData []byte, devMode bool) error {
	if config == nil {
		return errControllerDisabled
	}
	connManager := tsrv.ConnManager()

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
	// Empty path: the slice lands in whatever workspace `config` addresses,
	// which is also where the APIExport is; the SDK resolves that workspace's
	// canonical path and writes it explicitly (a path-less slice is missed by
	// kcp's per-shard endpoint publisher). Hardcoding the platform path here
	// would make this provider unable to run as an org's self-hosted copy.
	if err := sdkinstall.EnsureAPIExportEndpointSlice(ctx, dynCl, endpointSliceName, apiExportName, ""); err != nil {
		log.Printf("edge controller manager: WARNING could not ensure APIExportEndpointSlice: %v", err)
	}

	provider, err := apiexportprovider.New(config, endpointSliceName, apiexportprovider.Options{Scheme: s})
	if err != nil {
		return fmt.Errorf("creating apiexport multicluster provider: %w", err)
	}

	mgr, err := mcmanager.New(config, provider, manager.Options{
		Scheme:  s,
		Metrics: metricsserver.Options{BindAddress: "0"}, // provider serves its own HTTP
		Cache: cache.Options{
			// The local cache is only used for the tunnel registry Leases the
			// lifecycle reconcilers watch; restrict the Lease informer to them
			// so presence leases and any leader-election lease stay out of it.
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

	// Wire the tunnel plane's cross-workspace tenant reads/writes to this
	// manager's APIExport virtual workspace. The provider's own SA credential is
	// workspace-scoped, so re-rooting it to /clusters/<tenant> is rejected by kcp
	// — which broke agent join-token registration in production. mgr.GetCluster
	// engages each tenant logical cluster through the VW, the provider's only
	// credential with cross-workspace access to the bound Edge resources.
	tsrv.SetTenantConfigGetter(func(ctx context.Context, clusterName string) (*rest.Config, error) {
		cl, err := mgr.GetCluster(ctx, mcmulticluster.ClusterName(clusterName))
		if err != nil {
			return nil, fmt.Errorf("engaging tenant cluster %q: %w", clusterName, err)
		}
		return cl.GetConfig(), nil
	})

	opts := edgectrl.Options{HubExternalURL: hubExternalURL, HubCAData: hubCAData, DevMode: devMode}
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
		return fmt.Errorf("Workload scheduler: %w", err)
	}
	if err := status.SetupWithManager(mgr); err != nil {
		return fmt.Errorf("Workload status aggregator: %w", err)
	}

	// Edge event subscribers (currently UniFi Protect): a per-tenant, per-service
	// event store the validation reconciler feeds via WebSocket subscribers, and
	// the MCP `events` tool reads. The in-memory store is bounded per service and
	// sits behind an interface so it can be swapped for Redis (or another shared
	// backend). Multi-replica caveat: subscribers are gated to the replica that
	// terminates the edge's tunnel, so events buffer on THAT replica only — an
	// `events` MCP call the Service hands to a different replica sees an empty
	// buffer until the store moves to a shared backend. Both the writer (manager)
	// and reader (tunnel Server) share the one store; subscriber goroutines live
	// under ctx, so they stop on shutdown.
	eventStore := events.NewMemoryStore(events.DefaultPerServiceCap, eventsMaxAge)
	eventsMgr := events.NewManager(ctx, eventStore, ctrl.Log.WithName("edge-events"))
	tsrv.SetEventStore(eventStore)

	// EdgeService controllers (LinuxServer and MacOSServer edges): the discovery
	// reconciler pulls host services from each connected agent and materializes an
	// EdgeService per service; the validation reconciler checks configured
	// credentials against the service and stamps status. Both share the tunnel
	// ConnManager for agent dials.
	if err := servicectrl.SetupWithManager(mgr, connManager, servicectrl.Options{
		EdgeProxyPublicPath: edgeProxyPublicPath,
		Events:              eventsMgr,
	}); err != nil {
		return fmt.Errorf("EdgeService controllers: %w", err)
	}

	// Add-on publisher (host edges): for a runner Addon whose AGENT has
	// reported Allowed=True and published the add-on's token Secret, derive the
	// Service through which the add-on is reachable. It deliberately publishes
	// nothing from hub-side intent alone — see internal/addonctrl and
	// docs/edge-addons.md.
	if err := addonctrl.SetupWithManager(mgr); err != nil {
		return fmt.Errorf("Addon controller: %w", err)
	}

	go func() {
		log.Printf("edges controller manager starting (endpointSlice=%s)", endpointSliceName)
		if err := mgr.Start(ctx); err != nil {
			log.Printf("edge controller manager exited: %v", err)
		}
	}()
	return nil
}
