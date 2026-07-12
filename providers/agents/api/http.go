// Copyright 2026 The Faros Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package api

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"

	agentsclient "github.com/faroshq/provider-agents/client"
	"github.com/faroshq/provider-agents/store"
)

// identity carries the verified tenant context the hub injects on every proxied
// request. The hub authenticates the caller and resolves their workspace before
// forwarding, so these headers are trusted.
type identity struct {
	tenantPath    string // X-Kedge-Tenant, e.g. root:kedge:orgs:<org>:<ws>
	clusterID     string // X-Kedge-Cluster, the workspace's kcp logical-cluster ID
	orgUUID       string // parsed from tenantPath
	workspaceUUID string // parsed from tenantPath ("" when the path is org-only)
	user          string // X-Kedge-User
	token         string // bearer token, forwarded as-is from Authorization
}

// identityFromRequest extracts and validates the tenant context. It writes a
// 401 and returns ok=false when the tenant header is missing.
func identityFromRequest(w http.ResponseWriter, r *http.Request) (identity, bool) {
	tenantPath := strings.TrimSpace(r.Header.Get("X-Kedge-Tenant"))
	if tenantPath == "" {
		writeStatus(w, http.StatusUnauthorized, "Unauthorized", "tenant context missing — the hub did not resolve a workspace for this request")
		return identity{}, false
	}
	org, ws := parseTenantPath(tenantPath)
	return identity{
		tenantPath:    tenantPath,
		clusterID:     strings.TrimSpace(r.Header.Get("X-Kedge-Cluster")),
		orgUUID:       org,
		workspaceUUID: ws,
		user:          strings.TrimSpace(r.Header.Get("X-Kedge-User")),
		token:         bearerToken(r),
	}, true
}

// tenantPathPrefix is the kcp logical-cluster path prefix for tenant
// workspaces: root:kedge:tenants:<orgUUID> (org scope) or
// root:kedge:tenants:<orgUUID>:<ws> (workspace scope). See pkg/kcppaths.
const tenantPathPrefix = "root:kedge:tenants:"

// parseTenantPath splits a tenant path into its org and workspace segments.
// Returns ("", "") when the prefix doesn't match and (org, "") for an org-only
// path.
func parseTenantPath(p string) (org, workspace string) {
	rest := strings.TrimPrefix(p, tenantPathPrefix)
	if rest == p {
		return "", ""
	}
	parts := strings.Split(rest, ":")
	if len(parts) >= 1 {
		org = parts[0]
	}
	if len(parts) >= 2 {
		workspace = parts[1]
	}
	return org, workspace
}

// bearerToken returns the token from an Authorization: Bearer header, or "".
func bearerToken(r *http.Request) string {
	auth := r.Header.Get("Authorization")
	const p = "Bearer "
	if len(auth) > len(p) && strings.EqualFold(auth[:len(p)], p) {
		return strings.TrimSpace(auth[len(p):])
	}
	return ""
}

// statusResponse is the error envelope the portal expects.
type statusResponse struct {
	Kind    string `json:"kind"`
	Reason  string `json:"reason"`
	Message string `json:"message"`
	Code    int    `json:"code"`
}

func writeStatus(w http.ResponseWriter, code int, reason, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(statusResponse{
		Kind:    "Status",
		Reason:  reason,
		Message: message,
		Code:    code,
	})
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

// scope derives the store Scope for this identity, optionally narrowed to an
// agent.
func (id identity) scope(agentName string) store.Scope {
	return store.Scope{OrgUUID: id.orgUUID, WorkspaceUUID: id.workspaceUUID, AgentName: agentName}
}

// requireClient resolves the caller identity and a workspace-scoped tenant
// client. Returns ok=false (after writing the response) when the tenant context
// is incomplete or the provider has no hub URL configured.
func (s *Server) requireClient(w http.ResponseWriter, r *http.Request) (*agentsclient.Client, identity, bool) {
	id, ok := identityFromRequest(w, r)
	if !ok {
		return nil, identity{}, false
	}
	if s.gql == nil {
		writeStatus(w, http.StatusNotImplemented, "NotImplemented", "tenant access not configured — provider has no hub URL (set KEDGE_HUB_URL)")
		return nil, identity{}, false
	}
	if id.workspaceUUID == "" {
		writeStatus(w, http.StatusBadRequest, "BadRequest", "a workspace is required — select an organization and workspace first")
		return nil, identity{}, false
	}
	if id.clusterID == "" {
		writeStatus(w, http.StatusBadRequest, "BadRequest", "no workspace cluster on request (X-Kedge-Cluster missing) — the hub did not resolve a cluster")
		return nil, identity{}, false
	}
	scope, err := s.gql.For(id.clusterID, id.token)
	if err != nil {
		writeStatus(w, http.StatusInternalServerError, "InternalError", "creating tenant client: "+err.Error())
		return nil, identity{}, false
	}
	// Record the cluster→tenant mapping so background execution (which only
	// sees cluster IDs via the APIExport virtual workspace) writes transcripts
	// under the same scope the portal reads.
	_ = s.store.SaveTenantRef(r.Context(), id.clusterID, store.TenantRef{
		OrgUUID: id.orgUUID, WorkspaceUUID: id.workspaceUUID, UpdatedAt: time.Now().UTC(),
	})
	return agentsclient.NewFromGraphQL(scope), id, true
}
