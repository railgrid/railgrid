/*
Copyright 2026 The Faros Authors.

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
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
)

// identity is the per-request caller context the hub's backend proxy injects.
// The hub verifies the tenant before forwarding, so X-Kedge-Tenant is trusted;
// the org/workspace UUIDs are derived from it rather than from client-supplied
// headers (defense in depth — a spoofed header cannot mis-scope storage).
type identity struct {
	tenantPath    string // X-Kedge-Tenant, e.g. root:kedge:orgs:<org>:<ws>
	orgUUID       string // parsed from tenantPath
	workspaceUUID string // parsed from tenantPath ("" when the path is org-only)
	user          string // X-Kedge-User
	token         string // bearer token, forwarded as-is from Authorization
}

const tenantPathPrefix = "root:kedge:orgs:"

// identityFromRequest extracts the caller identity from the proxy-injected
// headers. It returns ok=false (and writes 401) when no tenant is present.
func identityFromRequest(w http.ResponseWriter, r *http.Request) (identity, bool) {
	tenantPath := strings.TrimSpace(r.Header.Get("X-Kedge-Tenant"))
	if tenantPath == "" {
		writeStatus(w, http.StatusUnauthorized, "Unauthorized", "tenant context missing — the hub did not resolve a workspace for this request")
		return identity{}, false
	}
	org, ws := parseTenantPath(tenantPath)
	return identity{
		tenantPath:    tenantPath,
		orgUUID:       org,
		workspaceUUID: ws,
		user:          strings.TrimSpace(r.Header.Get("X-Kedge-User")),
		token:         bearerToken(r),
	}, true
}

// parseTenantPath splits a root:kedge:orgs:<org>[:<ws>] cluster path into its
// org and workspace UUID segments.
func parseTenantPath(path string) (org, ws string) {
	rest := strings.TrimPrefix(path, tenantPathPrefix)
	if rest == path {
		return "", ""
	}
	parts := strings.Split(rest, ":")
	if len(parts) >= 1 {
		org = parts[0]
	}
	if len(parts) >= 2 {
		ws = parts[1]
	}
	return org, ws
}

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
