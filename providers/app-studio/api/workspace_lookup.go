/*
Copyright 2026 The Railgrid Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package api

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/railgrid/provider-sdk/dataplane"
	"github.com/railgrid/provider-sdk/tenantaccess"

	"github.com/railgrid/provider-app-studio/internal/crossprovider"
)

// The workspace behind a cluster ID — its path, and the org / workspace UUIDs
// App Studio keys durable state on — used to be read from the LogicalCluster
// as the caller. A verb carries no caller credential any more, and the
// provider identity has no standing on the shard's own /clusters/{id}; what
// it does have is its export virtual workspace, where kcp serves the
// APIBindings that bind THIS export. Each of those carries the consumer
// workspace's cluster (kcp.io/cluster) and path (kcp.io/path) annotations, so
// the binding for ai.railgrid.ai in cluster X is the authoritative answer to
// "which workspace is X" — the same read the Project reconciler makes
// (controller/project resolveLogicalClusterPath).

// appStudioAPIExportName is this provider's export, the one the tenant's
// APIBinding references.
const appStudioAPIExportName = "ai.railgrid.ai"

// ProviderWorkspaceResolver memoizes the APIBinding read per cluster ID. The
// mapping is a public property of the workspace, and every tenant operation
// afterwards is authorized by kcp on its own, so the cache is keyed by cluster
// alone. Only successful lookups are cached.
type ProviderWorkspaceResolver struct {
	callers dataplane.ProviderCallerFactory
	ttl     time.Duration

	mu  sync.RWMutex
	hot map[string]providerWorkspaceEntry
}

type providerWorkspaceEntry struct {
	ws        tenantaccess.Workspace
	expiresAt time.Time
}

// NewProviderWorkspaceResolver builds a resolver over the provider's caller
// factory. ttl <= 0 selects tenantaccess.DefaultWorkspaceResolverTTL.
func NewProviderWorkspaceResolver(callers dataplane.ProviderCallerFactory, ttl time.Duration) *ProviderWorkspaceResolver {
	if ttl <= 0 {
		ttl = tenantaccess.DefaultWorkspaceResolverTTL
	}
	return &ProviderWorkspaceResolver{callers: callers, ttl: ttl, hot: map[string]providerWorkspaceEntry{}}
}

// Resolve returns the workspace behind clusterID, reading it as the provider
// on a cache miss.
func (r *ProviderWorkspaceResolver) Resolve(ctx context.Context, clusterID string) (tenantaccess.Workspace, error) {
	clusterID = strings.TrimSpace(clusterID)
	if clusterID == "" {
		return tenantaccess.Workspace{}, errors.New("cluster ID is empty")
	}
	if r == nil || r.callers == nil {
		return tenantaccess.Workspace{}, errNoWorkspaceLookup
	}
	now := time.Now()
	r.mu.RLock()
	e, ok := r.hot[clusterID]
	r.mu.RUnlock()
	if ok && now.Before(e.expiresAt) {
		return e.ws, nil
	}
	provider, err := r.callers.AsProvider(clusterID)
	if err != nil {
		return tenantaccess.Workspace{}, err
	}
	bindings, err := provider.Resource(crossprovider.APIBindingsGVR).List(ctx, metav1.ListOptions{})
	if err != nil {
		return tenantaccess.Workspace{}, fmt.Errorf("resolving workspace for cluster %q: list APIBindings through the export virtual workspace: %w", clusterID, err)
	}
	path, err := workspacePathFromBindings(bindings.Items, clusterID)
	if err != nil {
		return tenantaccess.Workspace{}, fmt.Errorf("resolving workspace for cluster %q: %w", clusterID, err)
	}
	ws := tenantaccess.Workspace{ClusterID: clusterID, Path: path}
	ws.OrgUUID, ws.WorkspaceUUID, _ = tenantaccess.ParseTenantPath(path)
	r.mu.Lock()
	r.hot[clusterID] = providerWorkspaceEntry{ws: ws, expiresAt: now.Add(r.ttl)}
	r.mu.Unlock()
	return ws, nil
}

// workspacePathFromBindings is the pure half of Resolve: the one binding for
// this export in clusterID, and the path its annotation carries. A binding
// whose kcp.io/cluster disagrees with the addressed cluster is refused rather
// than trusted — the virtual workspace should only ever serve the consumer's
// own bindings under /clusters/{id}, and this is the check that it did.
func workspacePathFromBindings(items []unstructured.Unstructured, clusterID string) (string, error) {
	var match *unstructured.Unstructured
	for i := range items {
		binding := &items[i]
		export, _, _ := unstructured.NestedString(binding.Object, "spec", "reference", "export", "name")
		if strings.TrimSpace(export) != appStudioAPIExportName {
			continue
		}
		if match != nil {
			return "", fmt.Errorf("multiple APIBindings reference APIExport %q", appStudioAPIExportName)
		}
		match = binding
	}
	if match == nil {
		return "", fmt.Errorf("no APIBinding references APIExport %q; the workspace has not enabled this provider", appStudioAPIExportName)
	}
	annotations := match.GetAnnotations()
	if got := strings.TrimSpace(annotations[tenantaccess.LogicalClusterIDAnnotation]); got == "" {
		return "", fmt.Errorf("the App Studio APIBinding has no %s annotation", tenantaccess.LogicalClusterIDAnnotation)
	} else if got != clusterID {
		return "", fmt.Errorf("the App Studio APIBinding cluster %q does not match the addressed cluster %q", got, clusterID)
	}
	path := strings.TrimSpace(annotations[tenantaccess.LogicalClusterPathAnnotation])
	if path == "" {
		return "", fmt.Errorf("the App Studio APIBinding has no %s annotation", tenantaccess.LogicalClusterPathAnnotation)
	}
	return path, nil
}

// workspaceLookupFor returns the provider-scoped workspace lookup, or nil
// when the server has no provider credential (bare dev / tests).
func workspaceLookupFor(callers dataplane.ProviderCallerFactory) workspaceLookup {
	if callers == nil {
		return nil
	}
	return NewProviderWorkspaceResolver(callers, 0).Resolve
}
