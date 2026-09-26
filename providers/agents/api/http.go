// Copyright 2026 The Railgrid Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package api

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"strings"

	"github.com/railgrid/provider-sdk/tenantaccess"

	agentsclient "github.com/railgrid/provider-agents/client"
	"github.com/railgrid/provider-agents/store"
	"github.com/railgrid/provider-agents/tenant"
)

// identity carries the tenant context of one request.
//
// The tenant is identified by the workspace's kcp logical-cluster ID. On a
// data-plane verb it is the cluster segment of the path kcp forwarded, and the
// user is the identity kcp stamped (dataplane.ProxiedIdentityFrom); there is no
// bearer, so token is empty. On the MCP class the hub's aggregate still
// forwards the caller's bearer with X-Railgrid-Tenant/-Cluster/-User, and token
// is set.
//
// The organization / workspace UUIDs the store is keyed on are never parsed
// from a header: they come from kcp (the workspace's LogicalCluster, read as
// the caller when there is a caller credential) or from the cluster→workspace
// mapping this provider recorded — see resolveWorkspace and resolveClusterScope.
type identity struct {
	tenant        string // the workspace's kcp logical-cluster ID
	clusterID     string // the same ID; what the tenant client addresses
	workspacePath string // resolved from kcp, e.g. root:railgrid:tenants:<org>:<ws>; never from a header
	orgUUID       string // from workspacePath, or the recorded mapping
	workspaceUUID string // from workspacePath ("" for an organization workspace), or the recorded mapping
	workspaceErr  error  // why workspacePath could not be resolved, when it could not
	user          string // the caller's user name, for labels and audit lines; never a trust root
	token         string // the caller's bearer — MCP class only; a verb never has one
}

// workspaceLookup resolves a cluster ID to its workspace as the caller holding
// token. Production wires tenantaccess.WorkspaceResolver; tests substitute a
// table.
type workspaceLookup func(ctx context.Context, clusterID, token string) (tenantaccess.Workspace, error)

// identityFromRequest returns the tenant context for a data-plane verb.
//
// The router resolved it from the PATH and the caller kcp stamped, and stashed
// it (see dataplane.go). A request that carries no gate did not come through
// the router — nothing else is entitled to say which workspace a request
// addresses, and the hub's identity headers are never a trust root here — so
// it is refused with 401. The MCP transport reads its own identity with
// mcpIdentity; the OAuth callback and the webhooks carry none.
func (s *Server) identityFromRequest(w http.ResponseWriter, r *http.Request) (identity, bool) {
	if gate, ok := gateFrom(r.Context()); ok {
		return gate.identity, true
	}
	writeStatus(w, http.StatusUnauthorized, "Unauthorized", "no caller identity — this route is reached only as a kcp custom subresource")
	return identity{}, false
}

// unmappedOrg is the organization half of the fallback store scope for a
// cluster this provider has no recorded workspace mapping for. It pairs with
// the cluster ID as the workspace half, so rows written before and after a
// mapping is learned can be told apart; background.scopeFor writes under the
// same fallback.
const unmappedOrg = "unmapped"

// resolveClusterScope fills the org/workspace scope of id for a request that
// carries no caller credential — a data-plane verb.
//
// The recorded cluster→workspace mapping wins: it is what background execution
// (which likewise has no caller) writes transcripts under, so the two halves
// agree. Without one the scope is the cluster-keyed fallback background.scopeFor
// uses, never an error: a verb on a fresh workspace must work, and the cluster
// ID is a perfectly good key for rows that belong to exactly that workspace.
// Nothing here reads kcp as the caller, because there is no caller to read as.
func (s *Server) resolveClusterScope(ctx context.Context, id *identity) {
	if ref, ok, err := s.tenantRef(ctx, id.clusterID); err == nil && ok {
		id.orgUUID, id.workspaceUUID = ref.OrgUUID, ref.WorkspaceUUID
		return
	}
	id.orgUUID, id.workspaceUUID = unmappedOrg, id.clusterID
}

// resolveWorkspace fills the org/workspace scope of id from kcp, as the caller
// holding id.token — the MCP class, the one route that still carries a bearer.
// A missing lookup (no hub URL configured) or a failed one leaves the scope
// empty and records why in id.workspaceErr.
//
// A failed kcp read falls back to the cluster→workspace mapping this provider
// recorded the last time a caller who COULD read it came through. A data-plane
// verb never gets here: it has no token, and resolves its scope per cluster
// with resolveClusterScope.
func (s *Server) resolveWorkspace(ctx context.Context, id *identity) {
	if s == nil || s.workspaces == nil {
		id.workspaceErr = errNoWorkspaceLookup
		return
	}
	if id.clusterID == "" || id.token == "" {
		id.workspaceErr = errNoWorkspaceLookup
		return
	}
	ws, err := s.workspaces(ctx, id.clusterID, id.token)
	if err != nil {
		if ref, ok, refErr := s.tenantRef(ctx, id.clusterID); refErr == nil && ok {
			id.orgUUID, id.workspaceUUID = ref.OrgUUID, ref.WorkspaceUUID
			return
		}
		id.workspaceErr = err
		return
	}
	id.workspacePath, id.orgUUID, id.workspaceUUID = ws.Path, ws.OrgUUID, ws.WorkspaceUUID
}

// tenantRef reads the recorded cluster→workspace mapping, tolerating a Server
// assembled without a store (unit tests of the header plumbing alone).
func (s *Server) tenantRef(ctx context.Context, clusterID string) (store.TenantRef, bool, error) {
	if s.store == nil {
		return store.TenantRef{}, false, nil
	}
	return s.store.GetTenantRef(ctx, clusterID)
}

// errNoWorkspaceLookup is the workspaceErr when there is nothing to ask: no
// hub URL configured, or the request carries no cluster ID / token.
var errNoWorkspaceLookup = errNoLookup("workspace lookup unavailable (no hub URL configured or no caller credentials on the request)")

type errNoLookup string

func (e errNoLookup) Error() string { return string(e) }

// bearerToken returns the token from an Authorization: Bearer header, or "".
func bearerToken(r *http.Request) string {
	auth := r.Header.Get("Authorization")
	const p = "Bearer "
	if len(auth) > len(p) && strings.EqualFold(auth[:len(p)], p) {
		return strings.TrimSpace(auth[len(p):])
	}
	return ""
}

// detachedStreamContext splits a streaming request in two: a context for the
// WORK, which survives the client hanging up, and a predicate reporting whether
// the client is still there to write to.
//
// Streaming handlers otherwise couple the two, so closing a tab cancels the run
// behind it. That is right for a cheap read and wrong for anything that spends
// real money or minutes — the result is already durable (transcript + run
// record), so the only thing a disconnect should stop is the writing.
//
// Callers must consult clientGone before every write; nothing here prevents a
// write to a dead connection.
func detachedStreamContext(r *http.Request) (runCtx context.Context, clientGone func() bool) {
	reqCtx := r.Context()
	return context.WithoutCancel(reqCtx), func() bool { return reqCtx.Err() != nil }
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

// writeList writes a collection response, normalizing a nil slice to an empty
// JSON array. Go marshals a nil slice as null, which faults any client that
// maps over the result — an empty workspace is not an error case.
func writeList[T any](w http.ResponseWriter, items []T, extra ...map[string]any) {
	if items == nil {
		items = []T{}
	}
	body := map[string]any{"items": items}
	for _, e := range extra {
		for k, v := range e {
			body[k] = v
		}
	}
	writeJSON(w, http.StatusOK, body)
}

// scope derives the store Scope for this identity, optionally narrowed to an
// agent.
func (id identity) scope(agentName string) store.Scope {
	return store.Scope{OrgUUID: id.orgUUID, WorkspaceUUID: id.workspaceUUID, AgentName: agentName}
}

// requireClient resolves the identity of a gated request and the client its
// handler acts through. Returns ok=false (after writing the response) when the
// request did not come through the data-plane router or its tenant context is
// incomplete.
func (s *Server) requireClient(w http.ResponseWriter, r *http.Request) (*agentsclient.Client, identity, bool) {
	id, ok := s.identityFromRequest(w, r)
	if !ok {
		return nil, identity{}, false
	}
	gate, ok := gateFrom(r.Context())
	if !ok || gate.provider == nil {
		writeStatus(w, http.StatusInternalServerError, "InternalError", "the data-plane gate resolved no provider client for this request")
		return nil, identity{}, false
	}
	if id.clusterID == "" {
		writeStatus(w, http.StatusBadRequest, "BadRequest", "no workspace cluster on request — the path names no logical cluster")
		return nil, identity{}, false
	}
	if id.workspaceUUID == "" {
		if id.workspaceErr != nil {
			log.Printf("agents: resolving workspace for cluster %s: %v", id.clusterID, id.workspaceErr)
			writeStatus(w, http.StatusBadGateway, "WorkspaceUnresolved", "could not resolve the workspace behind cluster "+id.clusterID+": "+id.workspaceErr.Error())
			return nil, identity{}, false
		}
		writeStatus(w, http.StatusBadRequest, "BadRequest", "a workspace is required — cluster "+id.clusterID+" is an organization workspace; select a workspace first")
		return nil, identity{}, false
	}
	// The client is THE PROVIDER's, through its APIExport virtual workspace,
	// scoped to the path's cluster: the gate already settled that the caller
	// may see the addressed object, and there is no caller credential to act
	// with. Anything a handler reads or writes from here on is what the
	// provider's export and claims let it see — its own kinds, and Secrets
	// carrying the railgrid.ai/owner: agents label.
	return agentsclient.NewFromScope(tenant.NewScopeFromDynamic(id.clusterID, gate.provider)), id, true
}
