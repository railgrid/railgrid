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
package queryapi

import (
	"encoding/json"
	"net/http"
	"os"
	"strings"

	"github.com/faroshq/kuery/apis/query/v1alpha1"
	"github.com/faroshq/kuery/pkg/engine"

	"github.com/faroshq/provider-kuery/engagement"
)

// Handler serves POST /api/query.
type Handler struct {
	Engine *engine.Engine
}

// Identity is the hub-injected caller identity. The hub's backend proxy
// sets X-Kedge-Tenant from the authenticated request; without it (direct
// pod access) queries are refused.
type Identity struct {
	Tenant string
	User   string
}

// IdentityFromRequest extracts the proxy-injected identity. With
// KEDGE_DEV_ALLOW_TENANT_QUERY=true (dev only), ?tenant= substitutes for
// the header — same escape hatch as the infrastructure provider.
func IdentityFromRequest(r *http.Request) Identity {
	id := Identity{
		Tenant: r.Header.Get("X-Kedge-Tenant"),
		User:   r.Header.Get("X-Kedge-User"),
	}
	if os.Getenv("KEDGE_DEV_ALLOW_TENANT_QUERY") == "true" && id.Tenant == "" {
		id.Tenant = r.URL.Query().Get("tenant")
	}
	return id
}

// ServeHTTP handles POST /api/query with a v1alpha1.QuerySpec body and
// responds with the v1alpha1.QueryStatus JSON.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	id := IdentityFromRequest(r)
	if id.Tenant == "" {
		http.Error(w, "missing tenant identity (X-Kedge-Tenant)", http.StatusUnauthorized)
		return
	}

	var spec v1alpha1.QuerySpec
	if err := json.NewDecoder(r.Body).Decode(&spec); err != nil {
		http.Error(w, "invalid QuerySpec body: "+err.Error(), http.StatusBadRequest)
		return
	}

	ScopeToTenant(&spec, id.Tenant)

	status, err := h.Engine.Execute(r.Context(), &spec)
	if err != nil {
		// Engine errors are caller errors (validation) or store errors;
		// kuery wraps validation distinctly but a 400 for both keeps the
		// store's failure modes from leaking. The message is safe — it
		// never embeds other tenants' data.
		http.Error(w, "query failed: "+err.Error(), http.StatusBadRequest)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(status); err != nil {
		// Too late for an error status; connection-level failure.
		return
	}
}

// ScopeToTenant force-rewrites the spec's cluster filter so it can only
// match clusters engaged for this tenant:
//
//   - The labels map is REPLACED with exactly {tenant: <caller's tenant>}.
//     Replaced, not merged: cluster labels are an internal scoping
//     mechanism (engaged clusters carry engagement.TenantLabel), and on
//     SQLite kuery interpolates caller-controlled label KEYS into the SQL
//     json_extract path — merging would hand callers that string.
//   - A caller-supplied cluster name is interpreted as the EDGE name and
//     rewritten to the engaged form "{tenant}/{edge}". Already-prefixed
//     names are normalized to the caller's own tenant.
func ScopeToTenant(spec *v1alpha1.QuerySpec, tenant string) {
	if spec.Cluster == nil {
		spec.Cluster = &v1alpha1.ClusterFilter{}
	}
	spec.Cluster.Labels = map[string]string{engagement.TenantLabel: tenant}
	if name := spec.Cluster.Name; name != "" {
		edge := name
		if i := strings.LastIndex(name, "/"); i != -1 {
			edge = name[i+1:]
		}
		spec.Cluster.Name = tenant + "/" + edge
	}
}
