/*
Copyright 2026 The Faros Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package tenantaccess

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
)

// The hub identifies a tenant to a provider by the workspace's kcp
// logical-cluster ID — the same value in X-Faros-Tenant and X-Faros-Cluster —
// never by the workspace path. A provider that needs the path anyway (to
// derive the organization / workspace UUIDs it keys durable state on, or to
// hand a workload identity its tenant path) asks kcp: the workspace's
// LogicalCluster carries the canonical path in its kcp.io/path annotation,
// readable as the caller over {hub}/clusters/{clusterID}. This file is that
// lookup, plus the parser for the platform's tenant path layout.

// LogicalClusterGVR is kcp's per-workspace singleton, named "cluster".
var LogicalClusterGVR = schema.GroupVersionResource{
	Group: "core.kcp.io", Version: "v1alpha1", Resource: "logicalclusters",
}

const (
	// LogicalClusterPathAnnotation carries a workspace's canonical path.
	LogicalClusterPathAnnotation = "kcp.io/path"
	// LogicalClusterIDAnnotation carries a workspace's logical-cluster ID.
	LogicalClusterIDAnnotation = "kcp.io/cluster"
	// TenantPathPrefix is the parent of every Organization workspace; a tenant
	// path is root:faros:tenants:<orgUUID>[:<workspaceUUID>].
	TenantPathPrefix = "root:faros:tenants:"
	// DefaultWorkspaceResolverTTL bounds how long a WorkspaceResolver keeps a
	// (clusterID → Workspace) mapping. The mapping is stable for a workspace's
	// lifetime; the TTL only lets a deleted-and-recreated ID age out.
	DefaultWorkspaceResolverTTL = 10 * time.Minute
)

// Workspace is what a logical-cluster ID resolves to.
type Workspace struct {
	// ClusterID is the kcp logical-cluster ID the lookup was made for.
	ClusterID string
	// Path is the workspace's canonical kcp path (kcp.io/path).
	Path string
	// OrgUUID is the Organization segment of Path.
	OrgUUID string
	// WorkspaceUUID is the child-workspace segment of Path; empty for an
	// Organization workspace itself.
	WorkspaceUUID string
}

// WorkspacePath returns the canonical path of the workspace dyn is scoped to,
// from its LogicalCluster's kcp.io/path annotation.
func WorkspacePath(ctx context.Context, dyn dynamic.Interface) (string, error) {
	lc, err := dyn.Resource(LogicalClusterGVR).Get(ctx, "cluster", metav1.GetOptions{})
	if err != nil {
		return "", fmt.Errorf("getting LogicalCluster: %w", err)
	}
	path := strings.TrimSpace(lc.GetAnnotations()[LogicalClusterPathAnnotation])
	if path == "" {
		return "", errors.New("LogicalCluster carries no " + LogicalClusterPathAnnotation + " annotation")
	}
	return path, nil
}

// ParseTenantPath splits a tenant workspace path into its organization and
// workspace UUIDs. ok is false when the path is not under TenantPathPrefix or
// nests deeper than one child workspace; workspaceUUID is empty for an
// Organization workspace.
func ParseTenantPath(path string) (orgUUID, workspaceUUID string, ok bool) {
	rest, found := strings.CutPrefix(strings.TrimSpace(path), TenantPathPrefix)
	if !found || rest == "" {
		return "", "", false
	}
	parts := strings.Split(rest, ":")
	switch {
	case len(parts) == 1 && parts[0] != "":
		return parts[0], "", true
	case len(parts) == 2 && parts[0] != "" && parts[1] != "":
		return parts[0], parts[1], true
	default:
		return "", "", false
	}
}

// ResolveWorkspace reads the workspace behind clusterID as the caller holding
// token, via the hub's kcp proxy at {hubBase}/clusters/{clusterID}.
func ResolveWorkspace(ctx context.Context, hubBase, clusterID, token string, insecure bool) (Workspace, error) {
	dyn, err := NewDynamicClient(hubBase, clusterID, token, insecure)
	if err != nil {
		return Workspace{}, err
	}
	path, err := WorkspacePath(ctx, dyn)
	if err != nil {
		return Workspace{}, fmt.Errorf("resolving workspace for cluster %q: %w", clusterID, err)
	}
	ws := Workspace{ClusterID: clusterID, Path: path}
	ws.OrgUUID, ws.WorkspaceUUID, _ = ParseTenantPath(path)
	return ws, nil
}

// WorkspaceResolver memoizes ResolveWorkspace per cluster ID. A provider
// serving many requests per workspace pays the LogicalCluster read once per
// TTL rather than once per request. Only successful lookups are cached, so a
// transient hub error self-heals on the next request.
//
// The cache is keyed by cluster ID alone: the mapping is a public property of
// the workspace (the hub's own REST API returns both), and every tenant
// operation the provider performs afterwards is still authorized by kcp with
// the caller's own token.
type WorkspaceResolver struct {
	hubBase  string
	insecure bool
	ttl      time.Duration

	mu  sync.RWMutex
	hot map[string]workspaceEntry
}

type workspaceEntry struct {
	ws        Workspace
	expiresAt time.Time
}

// NewWorkspaceResolver returns a resolver over the hub at hubBase. ttl <= 0
// selects DefaultWorkspaceResolverTTL.
func NewWorkspaceResolver(hubBase string, insecure bool, ttl time.Duration) *WorkspaceResolver {
	if ttl <= 0 {
		ttl = DefaultWorkspaceResolverTTL
	}
	return &WorkspaceResolver{hubBase: strings.TrimRight(hubBase, "/"), insecure: insecure, ttl: ttl, hot: map[string]workspaceEntry{}}
}

// Resolve returns the workspace behind clusterID, reading it as the caller
// holding token on a cache miss.
func (r *WorkspaceResolver) Resolve(ctx context.Context, clusterID, token string) (Workspace, error) {
	clusterID = strings.TrimSpace(clusterID)
	if clusterID == "" {
		return Workspace{}, errors.New("cluster ID is empty")
	}
	now := time.Now()
	r.mu.RLock()
	e, ok := r.hot[clusterID]
	r.mu.RUnlock()
	if ok && now.Before(e.expiresAt) {
		return e.ws, nil
	}
	ws, err := ResolveWorkspace(ctx, r.hubBase, clusterID, token, r.insecure)
	if err != nil {
		return Workspace{}, err
	}
	r.mu.Lock()
	r.hot[clusterID] = workspaceEntry{ws: ws, expiresAt: now.Add(r.ttl)}
	r.mu.Unlock()
	return ws, nil
}
