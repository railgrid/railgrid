// Copyright 2026 The Faros Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

// Package queryapi is the ONLY entry point to the kuery store. It takes a
// kuery QuerySpec over HTTP and force-rewrites its cluster filter to the
// caller's tenant before handing it to the engine — kuery itself has no
// authorization, so isolation lives entirely at this choke point.
//
// Tenant identity is the tenant workspace's kcp logical-cluster ID,
// everywhere: the engagement controller keys engaged clusters
// "{clusterID}/{edge}" and labels their rows with the ID, and every query
// surface scopes by the ID the hub injects. Workspace paths are never
// identity — a path in an identity header is rejected, not translated.
package queryapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"regexp"
	"strings"

	"k8s.io/klog/v2"

	"github.com/faroshq/kuery/apis/query/v1alpha1"
	"github.com/faroshq/kuery/pkg/engine"

	"github.com/faroshq/provider-kuery/engagement"
)

// Handler serves POST /api/query.
type Handler struct {
	Engine *engine.Engine
}

// Identity is the hub-injected caller identity: the tenant workspace's kcp
// logical-cluster ID plus the user. Both hub paths carry the ID — the
// backend proxy (/services/providers/kuery/*) and the MCP aggregate's
// federation client inject X-Faros-Cluster on every request, and
// X-Faros-Tenant carries the same ID. Without an identity (direct pod
// access) requests are refused.
type Identity struct {
	// Cluster is the tenant's kcp logical-cluster ID — the tenant key kuery
	// scopes by.
	Cluster string
	User    string
}

var (
	// ErrMissingIdentity is returned when no identity header identifies the
	// caller's tenant.
	ErrMissingIdentity = errors.New("missing tenant identity (X-Faros-Cluster)")
	// ErrInvalidIdentity is returned when an identity header carries
	// something other than a kcp logical-cluster ID — typically a workspace
	// path (root:faros:tenants:...), which kuery never accepts as a tenant
	// key.
	ErrInvalidIdentity = errors.New("invalid tenant identity")
)

// clusterIDPattern is the shape of a kcp logical-cluster name: a lowercase
// DNS-label-like identifier (kcp mints 16-char base36 names; "root" is the
// root cluster). Workspace paths contain ":" and never match.
var clusterIDPattern = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`)

// IsClusterID reports whether s has the shape of a kcp logical-cluster ID.
func IsClusterID(s string) bool {
	return clusterIDPattern.MatchString(s)
}

// IdentityFromRequest extracts the proxy-injected identity. The cluster ID
// is taken from X-Faros-Cluster; X-Faros-Tenant is consulted only when that
// header is absent, and only if it holds a cluster ID — a workspace path
// there is an error, not a fallback (kuery keys nothing by path).
//
// With FAROS_DEV_ALLOW_TENANT_QUERY=true (dev only), ?tenant=<clusterID>
// substitutes for the headers — same escape hatch as the infrastructure
// provider.
func IdentityFromRequest(r *http.Request) (Identity, error) {
	id := Identity{User: r.Header.Get("X-Faros-User")}

	if v := strings.TrimSpace(r.Header.Get("X-Faros-Cluster")); v != "" {
		if !IsClusterID(v) {
			return id, fmt.Errorf("%w: X-Faros-Cluster %q is not a kcp logical-cluster ID", ErrInvalidIdentity, v)
		}
		id.Cluster = v
		return id, nil
	}
	if v := strings.TrimSpace(r.Header.Get("X-Faros-Tenant")); v != "" {
		if !IsClusterID(v) {
			return id, fmt.Errorf("%w: X-Faros-Tenant %q is a workspace path, not a kcp logical-cluster ID; kuery identifies tenants by cluster ID only (send X-Faros-Cluster)", ErrInvalidIdentity, v)
		}
		id.Cluster = v
		return id, nil
	}
	if os.Getenv("FAROS_DEV_ALLOW_TENANT_QUERY") == "true" {
		if v := strings.TrimSpace(r.URL.Query().Get("tenant")); v != "" {
			if !IsClusterID(v) {
				return id, fmt.Errorf("%w: ?tenant=%q is not a kcp logical-cluster ID", ErrInvalidIdentity, v)
			}
			id.Cluster = v
			return id, nil
		}
	}
	return id, ErrMissingIdentity
}

// writeIdentityError maps an IdentityFromRequest failure to an HTTP status:
// no identity is 401, a malformed one (a path) is 400 — the proxy sent
// something kuery cannot scope by, and retrying with the same headers
// cannot succeed.
func writeIdentityError(w http.ResponseWriter, err error) {
	status := http.StatusUnauthorized
	if errors.Is(err, ErrInvalidIdentity) {
		status = http.StatusBadRequest
	}
	http.Error(w, err.Error(), status)
}

// ServeHTTP handles POST /api/query with a v1alpha1.QuerySpec body and
// responds with the v1alpha1.QueryStatus JSON.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	id, err := IdentityFromRequest(r)
	if err != nil {
		writeIdentityError(w, err)
		return
	}

	var spec v1alpha1.QuerySpec
	if err := json.NewDecoder(r.Body).Decode(&spec); err != nil {
		http.Error(w, "invalid QuerySpec body: "+err.Error(), http.StatusBadRequest)
		return
	}

	ScopeToTenant(&spec, id.Cluster)

	status, err := h.Engine.Execute(r.Context(), &spec)
	if err != nil {
		// The engine prefixes caller-controlled validation failures with
		// "validation:" and wraps everything else (SQL generation, query
		// execution, scanning) under its own prefixes. Validation messages
		// are safe and actionable, so echo them as a 400. Internal store
		// failures, however, can leak confusing driver internals to users
		// (e.g. "UNION types ... cannot be matched (SQLSTATE 42804)"), which
		// are kuery engine bugs, not something the user can act on. Log the
		// full detail server-side and return a generic message.
		if strings.HasPrefix(err.Error(), "validation:") {
			http.Error(w, "query failed: "+err.Error(), http.StatusBadRequest)
			return
		}
		klog.FromContext(r.Context()).Error(err, "kuery query execution failed",
			"tenant", id.Cluster, "user", id.User)
		http.Error(w, "query failed: internal error", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(status); err != nil {
		// Too late for an error status; connection-level failure.
		return
	}
}

// ScopeToTenant force-rewrites the spec's cluster filter so it can only
// match clusters engaged for this tenant, identified by its kcp
// logical-cluster ID:
//
//   - The labels map is REPLACED with exactly {tenant: <cluster ID>}.
//     Replaced, not merged: cluster labels are an internal scoping
//     mechanism (engaged clusters carry engagement.TenantLabel), and on
//     SQLite kuery interpolates caller-controlled label KEYS into the SQL
//     json_extract path — merging would hand callers that string.
//   - A caller-supplied cluster name is interpreted as the EDGE name and
//     rewritten to the engaged form "{clusterID}/{edge}". Already-prefixed
//     names are normalized to the caller's own tenant.
func ScopeToTenant(spec *v1alpha1.QuerySpec, cluster string) {
	if spec.Cluster == nil {
		spec.Cluster = &v1alpha1.ClusterFilter{}
	}
	spec.Cluster.Labels = map[string]string{engagement.TenantLabel: cluster}
	if name := spec.Cluster.Name; name != "" {
		edge := name
		if i := strings.LastIndex(name, "/"); i != -1 {
			edge = name[i+1:]
		}
		spec.Cluster.Name = cluster + "/" + edge
	}
}
