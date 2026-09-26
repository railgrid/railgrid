// Copyright 2026 The Railgrid Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

// Package queryapi is the ONLY entry point to the kuery store.
//
// There is exactly one route into it — the query verb on a named SavedView,
// the kcp custom subresource savedviews/run on kuery's APIExport,
// POST /clusters/{clusterID}/apis/kuery.providers.railgrid.ai/v1alpha1/savedviews/{name}/run
// (run.go). kcp authenticates the caller and authorizes the verb with ordinary
// RBAC before forwarding the request with the caller's identity stamped in
// requestheader headers; the handler then settles visibility of the SavedView
// on the caller's behalf and acts as the provider. What used to be here
// instead was a flat POST /api/query whose tenant came from the
// X-Railgrid-Cluster header and whose bearer was never looked at; it was safe
// only for as long as the hub proxy stripped and re-injected that header, and
// a request sent straight at the pod would have been believed. It is gone,
// along with /api/edges, /api/status, the RAILGRID_DEV_ALLOW_TENANT_QUERY
// "?tenant=" escape hatch and, later, the hub-proxied /dataplane/ spelling of
// the verb. No compatibility route replaced any of them.
//
// Tenant identity is the tenant workspace's kcp logical-cluster ID, and it
// comes from the request PATH: the engagement controller keys engaged clusters
// "{clusterID}/{edge}", the Engagement records key on the same ID, and the
// gate runs against that same ID. Workspace paths are never identity — the
// data-plane grammar refuses one in the cluster position rather than
// translating it.
package queryapi

import (
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/railgrid/kuery/apis/query/v1alpha1"

	"github.com/railgrid/provider-kuery/index"
)

// clusterIDPattern is the shape of a kcp logical-cluster name: a lowercase
// DNS-label-like identifier (kcp mints 16-char base36 names; "root" is the
// root cluster). Workspace paths contain ":" and never match.
var clusterIDPattern = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`)

// IsClusterID reports whether s has the shape of a kcp logical-cluster ID.
func IsClusterID(s string) bool {
	return clusterIDPattern.MatchString(s)
}

var (
	// ErrNoEngagedEdges means the tenant has no edge kuery is currently
	// syncing, so there is nothing any query of theirs could match.
	ErrNoEngagedEdges = errors.New("no edges are engaged for this workspace")
	// ErrEdgeNotEngaged means the query named an edge that is not in the
	// tenant's engaged set — a typo, an edge that just disconnected, or
	// another tenant's edge.
	ErrEdgeNotEngaged = errors.New("edge is not engaged for this workspace")
)

// ScopeToTenant force-rewrites the spec's cluster filter so it can only match
// edges engaged for this tenant, identified by its kcp logical-cluster ID, and
// refuses outright when there is nothing to match.
//
// engaged is the tenant's edge set, read from the Engagement records — the
// authority. The rewrite below is how that answer is expressed to an engine
// whose ClusterFilter holds one cluster name or one label map:
//
//   - With no engaged edge, the query is refused before the store is touched.
//     It is not "an empty result": the caller asked about a fleet that is not
//     being synced, and saying so is the only useful answer.
//   - A caller-supplied cluster name is interpreted as the EDGE name and must
//     be in the engaged set; it is then rewritten to the engaged form
//     "{clusterID}/{edge}". A name already carrying a prefix — the caller's
//     own, a foreign cluster's, or a legacy workspace path — is stripped back
//     to the edge and re-pinned to the caller's own cluster.
//   - The labels map is REPLACED with exactly {tenant: <cluster ID>}.
//     Replaced, not merged: on SQLite kuery interpolates caller-controlled
//     label KEYS into the SQL json_extract path, and merging would hand
//     callers that string.
func ScopeToTenant(spec *v1alpha1.QuerySpec, cluster string, engaged []string) error {
	if len(engaged) == 0 {
		return fmt.Errorf("%w", ErrNoEngagedEdges)
	}
	if spec.Cluster == nil {
		spec.Cluster = &v1alpha1.ClusterFilter{}
	}
	spec.Cluster.Labels = map[string]string{index.TenantLabel: cluster}

	if name := spec.Cluster.Name; name != "" {
		edge := index.EdgeOf(name)
		if !contains(engaged, edge) {
			return fmt.Errorf("%w: %q (engaged: %s)", ErrEdgeNotEngaged, edge, strings.Join(engaged, ", "))
		}
		spec.Cluster.Name = index.StoreName(cluster, edge)
	}
	return nil
}

func contains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
