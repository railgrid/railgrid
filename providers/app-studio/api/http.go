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

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/railgrid/provider-sdk/dataplane"
	"github.com/railgrid/provider-sdk/tenantaccess"
)

// identity is the per-request caller context the hub's backend proxy injects.
// The hub verifies the caller and resolves their workspace before forwarding,
// and identifies that workspace by its kcp logical-cluster ID: X-Railgrid-Tenant
// and X-Railgrid-Cluster both carry the ID, and the workspace path is never sent.
//
// The organization / workspace UUIDs App Studio keys durable state on, and the
// tenant path a development workload's Provider Actions identity needs, are
// therefore not parsed from any header. They are read from kcp — the
// workspace's LogicalCluster, as the caller — through Server.workspaces
// (defense in depth: a header value that merely looks like a path cannot
// mis-scope storage).
type identity struct {
	tenant        string // X-Railgrid-Tenant: the workspace's kcp logical-cluster ID
	clusterID     string // X-Railgrid-Cluster: the same ID; what the tenant client addresses
	workspacePath string // resolved from kcp, e.g. root:railgrid:tenants:<org>:<ws>; never from a header
	orgUUID       string // from workspacePath
	workspaceUUID string // from workspacePath ("" for an organization workspace)
	workspaceErr  error  // why workspacePath could not be resolved, when it could not
	// user is the AUTHENTICATED actor: the username kcp answers a
	// SelfSubjectReview with, for this request's own bearer, on this
	// request's cluster. Thread and attachment ownership, approval decisions
	// and audit records all key on it, so it is never read from a header.
	user string
	// userErr is why the actor could not be resolved, when it could not.
	userErr error
	// userLabel is X-Railgrid-User: a display hint from the hub, never an
	// identity. Nothing may authorize on it.
	userLabel string
	token     string // bearer token, forwarded as-is from Authorization
}

// workspaceLookup resolves a cluster ID to its workspace as the caller holding
// token. Production wires tenantaccess.WorkspaceResolver over the hub; tests
// substitute a table.
type workspaceLookup func(ctx context.Context, clusterID, token string) (tenantaccess.Workspace, error)

// ErrActorUnresolved is what requireProjectClient reports when the caller's
// identity could not be established. It is deliberately not a fallback to the
// header: an unverified actor is not an actor.
var ErrActorUnresolved = errors.New("caller identity could not be established")

// identityFromRequest extracts the caller identity from the proxy-injected
// headers. It returns ok=false (and writes 401) when no tenant is present.
// Workspace resolution is best-effort here: handlers that need the
// org/workspace scope check it (requireProjectClient) and report the
// resolution error, so an endpoint that only needs the cluster ID keeps
// working when the lookup is unavailable.
func (s *Server) identityFromRequest(w http.ResponseWriter, r *http.Request) (identity, bool) {
	// The workspace comes from the PATH: the data-plane dispatcher parsed it,
	// refused it when it disagreed with the hub's header, and ran both gates
	// against it before any handler saw the request. Reading it from a header
	// here would be reading a value nothing checked
	// (docs/provider-contract-review.md §3.7, "X-Railgrid-User is taken from
	// the header as the actor").
	cluster := dataPlaneCluster(r)
	if cluster == "" {
		cluster = strings.TrimSpace(r.Header.Get(dataplane.HeaderCluster))
	}
	id := identity{
		tenant:    cluster,
		clusterID: cluster,
		userLabel: strings.TrimSpace(r.Header.Get(dataplane.HeaderUser)),
		token:     bearerToken(r),
	}
	if id.clusterID == "" {
		writeStatus(w, http.StatusUnauthorized, "Unauthorized", "no workspace on this request — it did not arrive through the data plane")
		return identity{}, false
	}
	s.resolveWorkspace(r.Context(), &id)
	s.resolveActor(r.Context(), &id)
	// Every handler that resolves a caller can also touch that caller's
	// project working-copy ledger, which since §9 Cut D.3 is
	// `Project.status.workspace` rather than a file beside the tree
	// (api/project_ledger.go). Attaching it to the request here is what keeps
	// the ~20 workspace-store call sites free of control-plane plumbing; the
	// ledger is lazy, so a request that never touches it builds no client.
	*r = *r.WithContext(s.withProjectLedger(r.Context(), id))
	return id, true
}

// resolveActor fills id.user from a SelfSubjectReview against this request's
// cluster with this request's bearer, once per request and cached per token
// hash. A failure leaves the actor empty and records why; handlers that need
// an actor refuse rather than fall back to X-Railgrid-User.
func (s *Server) resolveActor(ctx context.Context, id *identity) {
	if s == nil || s.tenantActors == nil || id.clusterID == "" || id.token == "" {
		id.userErr = errNoActorLookup
		return
	}
	user, err := s.tenantActors(ctx, id.clusterID, id.token)
	if err != nil {
		id.userErr = err
		return
	}
	id.user = user
}

// resolveWorkspace fills the org/workspace scope of id from kcp. A missing
// lookup (no hub URL configured) or a failed one leaves the scope empty and
// records why in id.workspaceErr. The lookup is Server.tenantWorkspaces.
func (s *Server) resolveWorkspace(ctx context.Context, id *identity) {
	if s == nil || s.tenantWorkspaces == nil || id.clusterID == "" || id.token == "" {
		id.workspaceErr = errNoWorkspaceLookup
		return
	}
	ws, err := s.tenantWorkspaces(ctx, id.clusterID, id.token)
	if err != nil {
		id.workspaceErr = err
		return
	}
	id.workspacePath, id.orgUUID, id.workspaceUUID = ws.Path, ws.OrgUUID, ws.WorkspaceUUID
}

// errNoWorkspaceLookup is the workspaceErr when there is nothing to ask: no
// hub URL configured, or the request carries no cluster ID / token.
var errNoWorkspaceLookup = errors.New("workspace lookup unavailable (no hub URL configured or no caller credentials on the request)")

func bearerToken(r *http.Request) string {
	auth := strings.TrimSpace(r.Header.Get("Authorization"))
	if auth == "" {
		return ""
	}
	if strings.HasPrefix(strings.ToLower(auth), "bearer ") {
		return strings.TrimSpace(auth[len("bearer "):])
	}
	return auth
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
