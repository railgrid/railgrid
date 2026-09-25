/*
Copyright 2026 The Railgrid Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/dynamic"

	"github.com/railgrid/provider-sdk/dataplane"
	"github.com/railgrid/provider-sdk/tenantaccess"
)

// identity is the per-request caller context of a data-plane verb.
//
// A verb reaches this provider only as a kcp custom subresource: the shard
// authenticated the caller, authorized the verb with RBAC, and forwarded the
// request with the caller's identity stamped in requestheader headers, which
// serve's adapter read and put in the request context. There is no bearer
// here. The workspace is identified by its kcp logical-cluster ID from the
// PATH, and the workspace path is never sent by anyone.
//
// The organization / workspace UUIDs App Studio keys durable state on, and the
// tenant path a development workload's Provider Actions identity needs, are
// therefore not parsed from any header. They are read from kcp — through the
// provider's own APIExport virtual workspace — by Server.tenantWorkspaces.
type identity struct {
	tenant        string // the workspace's kcp logical-cluster ID (the path's cluster segment)
	clusterID     string // the same ID; what the tenant client addresses
	workspacePath string // resolved from kcp, e.g. root:railgrid:tenants:<org>:<ws>; never from a header
	orgUUID       string // from workspacePath
	workspaceUUID string // from workspacePath ("" for an organization workspace)
	workspaceErr  error  // why workspacePath could not be resolved, when it could not
	// user is the AUTHENTICATED actor: the username kcp stamped onto the
	// forwarded request after authenticating the caller itself
	// (dataplane.ProxiedIdentity.User). Thread and attachment ownership,
	// approval decisions and audit records all key on it, so it is never
	// read from an addressing header.
	user string
	// userErr is why the actor could not be resolved, when it could not.
	userErr error
	// userLabel is X-Railgrid-User: a display hint, never an identity.
	// Nothing may authorize on it.
	userLabel string
	// caller is the whole stamped identity — name, groups, extras — for the
	// SubjectAccessReviews a handler runs about the caller
	// (dataplane.Authorize). Every question about the caller beyond what the
	// gate settled is one of those; there is no caller credential to try. A
	// pointer so identity stays comparable; nil when no caller was stamped.
	caller *dataplane.ProxiedIdentity
	// provider is the client the gate returned: this provider acting in
	// clusterID through its export virtual workspace. Handlers act through it
	// (clientFor); nil when the request did not come through the dispatcher.
	provider dynamic.Interface
}

// workspaceLookup resolves a cluster ID to its workspace, as the provider.
// Production wires a resolver over the export virtual workspace
// (workspace_lookup.go); tests substitute a table.
type workspaceLookup func(ctx context.Context, clusterID string) (tenantaccess.Workspace, error)

// ErrActorUnresolved is what requireProjectClient reports when the caller's
// identity could not be established. It is deliberately not a fallback to the
// label header: an unverified actor is not an actor.
var ErrActorUnresolved = errors.New("caller identity could not be established")

// identityFromRequest extracts the caller identity from the request. It
// returns ok=false (and writes 401) when no cluster is present. Workspace
// resolution is best-effort here: handlers that need the org/workspace scope
// check it (requireProjectClient) and report the resolution error, so an
// endpoint that only needs the cluster ID keeps working when the lookup is
// unavailable.
func (s *Server) identityFromRequest(w http.ResponseWriter, r *http.Request) (identity, bool) {
	// The workspace comes from the PATH: serve's adapter parsed it and the
	// dispatcher ran the gate against it before any handler saw the request.
	// Reading it from a header here would be reading a value nothing checked
	// (docs/provider-contract-review.md §3.7, "X-Railgrid-User is taken from
	// the header as the actor").
	cluster := dataPlaneCluster(r)
	if cluster == "" {
		// Not a verb path. Nothing in production reaches a handler except
		// through the dispatcher, so this is only the retired /api route
		// fixture the handler tests drive (register_fixture_test.go); it
		// names the cluster the way the hub proxy once did.
		cluster = strings.TrimSpace(r.Header.Get(dataplane.HeaderCluster))
	}
	id := identity{
		tenant:    cluster,
		clusterID: cluster,
		userLabel: strings.TrimSpace(r.Header.Get(dataplane.HeaderUser)),
	}
	if id.clusterID == "" {
		writeStatus(w, http.StatusUnauthorized, "Unauthorized", "no workspace on this request — it did not arrive as a kcp custom subresource")
		return identity{}, false
	}
	if provider, ok := r.Context().Value(dataPlaneProviderKey{}).(dynamic.Interface); ok {
		id.provider = provider
	}
	s.resolveActor(r, &id)
	s.resolveWorkspace(r.Context(), &id)
	// Every handler that resolves a caller can also touch that caller's
	// project working-copy ledger, which since §9 Cut D.3 is
	// `Project.status.workspace` rather than a file beside the tree
	// (api/project_ledger.go). Attaching it to the request here is what keeps
	// the ~20 workspace-store call sites free of control-plane plumbing; the
	// ledger is lazy, so a request that never touches it builds no client.
	*r = *r.WithContext(s.withProjectLedger(r.Context(), id))
	return id, true
}

// resolveActor fills id.user and id.caller from the identity kcp stamped.
//
// After serve's adapter it is in the request context (the adapter is the one
// place that sets it). BEFORE the adapter — the replica-affinity layer runs
// first, because forwarding has to happen before the body is read — it is
// read from the same X-Remote-* headers the adapter reads, over the same
// trusted connection. A request carrying neither has no actor; handlers that
// need one refuse rather than fall back to X-Railgrid-User.
func (s *Server) resolveActor(r *http.Request, id *identity) {
	caller, ok := dataplane.ProxiedIdentityFrom(r.Context())
	if !ok {
		stamped, err := dataplane.ProxiedCaller(r)
		if err != nil {
			id.userErr = errNoActorLookup
			return
		}
		caller = stamped
	}
	if s != nil && s.tenantActors != nil {
		// Test seam: a table standing in for the shard's stamp.
		user, err := s.tenantActors(r.Context(), id.clusterID, caller)
		if err != nil {
			id.userErr = err
			return
		}
		caller.User = user
	}
	if strings.TrimSpace(caller.User) == "" {
		id.userErr = errNoActorLookup
		return
	}
	id.caller = &caller
	id.user = caller.User
}

// resolveWorkspace fills the org/workspace scope of id from kcp. A missing
// lookup (no provider credential) or a failed one leaves the scope empty and
// records why in id.workspaceErr. The lookup is Server.tenantWorkspaces.
func (s *Server) resolveWorkspace(ctx context.Context, id *identity) {
	if s == nil || s.tenantWorkspaces == nil || id.clusterID == "" {
		id.workspaceErr = errNoWorkspaceLookup
		return
	}
	ws, err := s.tenantWorkspaces(ctx, id.clusterID)
	if err != nil {
		id.workspaceErr = err
		return
	}
	id.workspacePath, id.orgUUID, id.workspaceUUID = ws.Path, ws.OrgUUID, ws.WorkspaceUUID
}

// errNoWorkspaceLookup is the workspaceErr when there is nothing to ask: no
// provider credential configured, or the request carries no cluster ID.
var errNoWorkspaceLookup = errors.New("workspace lookup unavailable (no provider credential configured or no cluster on the request)")

// setHubCallerHeaders stamps a request to the hub's OWN REST API or MCP
// aggregate — the provider catalog, the membership rosters, the browser
// handoff, the workspace MCP endpoint. Those are not data-plane verbs and a
// verb carries no caller credential to forward to them, so they are made as
// this provider (Server.hubToken) with the kcp-authenticated caller's name and
// the request's workspace selection as the headers the hub resolves a
// provider caller's scope from. A process with no hub token sends none, and
// the hub answers as it does any unauthenticated caller.
func (s *Server) setHubCallerHeaders(h http.Header, id identity) {
	if s != nil && s.hubToken != "" {
		h.Set("Authorization", "Bearer "+s.hubToken)
	}
	if id.tenant != "" {
		h.Set(dataplane.HeaderTenant, id.tenant)
	}
	if id.clusterID != "" {
		h.Set(dataplane.HeaderCluster, id.clusterID)
	}
	if id.orgUUID != "" {
		h.Set("X-Railgrid-Org", id.orgUUID)
	}
	if id.workspaceUUID != "" {
		h.Set("X-Railgrid-Workspace", id.workspaceUUID)
	}
	if id.user != "" {
		// A display label for the hub's and the downstream provider's logs;
		// the identity that authorizes the call is the provider's bearer.
		h.Set(dataplane.HeaderUser, id.user)
	}
}

// hubRequest clones r as the request the MCP helpers carry to the hub's MCP
// aggregate (projectMCPRequest copies its Authorization and X-Railgrid-*
// headers onto the call): a verb arrives with no Authorization — serve's
// adapter removed it — so the call is made as the provider, with the caller's
// name as a label.
func (s *Server) hubRequest(r *http.Request, id identity) *http.Request {
	if r == nil {
		return nil
	}
	out := r.Clone(r.Context())
	out.Header.Del("Authorization")
	s.setHubCallerHeaders(out.Header, id)
	return out
}

// hubHTTPClient is the client for hub REST calls: the same TLS knob the MCP
// client honours for an in-cluster hub certificate. The protocol tests' HTTP
// seam (sandboxDataPlaneClientFactory) stands in for it too, so a test that
// fakes the whole wire sees the hub call beside the data-plane ones.
func (s *Server) hubHTTPClient(timeout time.Duration) *http.Client {
	if s != nil && s.sandboxDataPlaneClientFactory != nil {
		return s.sandboxDataPlaneClientFactory(timeout)
	}
	insecure := s != nil && s.mcpInsecureSkipTLSVerify
	return &http.Client{Timeout: timeout, Transport: projectMCPTransport(insecure)}
}

// authorizeCaller asks kcp, on the caller's behalf, whether they may perform
// attrs — through the provider client the gate returned. It is how a handler
// decides anything about the caller beyond the gate: a second object the verb
// touches, a write the verb performs for them.
func (s *Server) authorizeCaller(ctx context.Context, id identity, attrs dataplane.ResourceAttributes) (bool, error) {
	provider := id.provider
	if provider == nil {
		if s == nil || s.callers == nil {
			return false, errors.New("no provider client to review the caller with")
		}
		p, err := s.callers.AsProvider(id.clusterID)
		if err != nil {
			return false, err
		}
		provider = p
	}
	if id.caller == nil {
		return false, dataplane.ErrNoCaller
	}
	return dataplane.Authorize(ctx, provider, *id.caller, attrs)
}

// decodeJSON unmarshals r.Body into out. 400 + writes status on error.
func decodeJSON(w http.ResponseWriter, r *http.Request, out any) bool {
	if err := json.NewDecoder(r.Body).Decode(out); err != nil {
		writeStatus(w, http.StatusBadRequest, "BadRequest", "invalid JSON body: "+err.Error())
		return false
	}
	return true
}

func decodeStrictJSON(w http.ResponseWriter, r *http.Request, out any) bool {
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(out); err != nil {
		writeStatus(w, http.StatusBadRequest, "BadRequest", "invalid JSON body: "+err.Error())
		return false
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			writeStatus(w, http.StatusBadRequest, "BadRequest", "invalid JSON body: must contain exactly one JSON value")
		} else {
			writeStatus(w, http.StatusBadRequest, "BadRequest", "invalid JSON body: trailing JSON: "+err.Error())
		}
		return false
	}
	return true
}

// decodeStrictJSONWithBodyLimit applies a route-local cap before using the
// shared strict decoder. The cap is deliberately opt-in so existing callers
// retain their established request-size behavior while annotation-bearing
// assistant starts can bound their structured payloads proportionally.
func decodeStrictJSONWithBodyLimit(w http.ResponseWriter, r *http.Request, out any, maxBytes int64) bool {
	if maxBytes > 0 {
		r.Body = http.MaxBytesReader(w, r.Body, maxBytes)
	}
	return decodeStrictJSON(w, r, out)
}

// writeError turns a kube/client error into a sensible HTTP code.
func writeError(w http.ResponseWriter, err error) {
	switch {
	case apierrors.IsNotFound(err):
		writeStatus(w, http.StatusNotFound, "NotFound", err.Error())
	case apierrors.IsAlreadyExists(err):
		writeStatus(w, http.StatusConflict, "Conflict", err.Error())
	case apierrors.IsConflict(err):
		writeStatus(w, http.StatusConflict, "Conflict", err.Error())
	case apierrors.IsInvalid(err), apierrors.IsBadRequest(err):
		writeStatus(w, http.StatusBadRequest, "BadRequest", err.Error())
	case apierrors.IsForbidden(err):
		writeStatus(w, http.StatusForbidden, "Forbidden", err.Error())
	default:
		var validationErr *ValidationError
		if errors.As(err, &validationErr) {
			writeStatus(w, http.StatusBadRequest, "BadRequest", err.Error())
			return
		}
		writeStatus(w, http.StatusInternalServerError, "InternalError", err.Error())
	}
}

// ValidationError is the sentinel for handler-side input validation failures.
// writeError translates it into 400. Use newValidationError to construct.
type ValidationError struct{ Msg string }

func (e *ValidationError) Error() string { return e.Msg }

func newValidationError(msg string) error { return &ValidationError{Msg: msg} }

// newConflictError is a handler-side 409 whose message is shown verbatim;
// writeError maps it through apierrors.IsConflict.
func newConflictError(msg string) error {
	return &apierrors.StatusError{ErrStatus: metav1.Status{
		Status:  metav1.StatusFailure,
		Code:    http.StatusConflict,
		Reason:  metav1.StatusReasonConflict,
		Message: msg,
	}}
}

// writeStatus emits a kubernetes-style Status envelope so kubectl-like clients
// render it nicely.
func writeStatus(w http.ResponseWriter, code int, reason, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	body := map[string]any{
		"kind":       "Status",
		"apiVersion": "v1",
		"metadata":   map[string]any{},
		"status":     "Failure",
		"message":    message,
		"reason":     reason,
		"code":       code,
	}
	_ = json.NewEncoder(w).Encode(body)
}

func writeJSON(w http.ResponseWriter, code int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(body)
}

// ListResponse is the envelope for list endpoints.
type ListResponse[T any] struct {
	Items []T `json:"items"`
}

// errorText renders an error for a status message, tolerating nil.
func errorText(err error) string {
	if err == nil {
		return "no reason recorded"
	}
	return err.Error()
}
