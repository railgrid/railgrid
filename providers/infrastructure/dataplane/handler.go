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

package dataplane

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// PathPrefix is where the handler is mounted on the provider's serve mux. It is
// reached through the hub backend proxy at
// /services/providers/infrastructure/dataplane/... with the caller's bearer
// token forwarded as-is and X-Kedge-* identity headers injected.
const PathPrefix = "/dataplane/"

// InstanceGetter authorizes and fetches a workload instance AS THE CALLER. The
// implementation builds a tenant-scoped client from (workspace, token) and does
// a GET — so a 403/404 from the caller's RBAC is the access gate for the whole
// data plane. No provider-wide credential is consulted here.
type InstanceGetter interface {
	Get(ctx context.Context, workspace, token, resource, name string) (*unstructured.Unstructured, error)
}

// Handler serves a template's declared data-plane verbs as subresources on a
// workload instance:
//
//	/dataplane/clusters/<ws>/<resource>/<name>/<verb>[/<caller-path...>]
//
// e.g. /dataplane/clusters/root:kedge:orgs:acme/sandboxrunners/kedge-sandbox-…/log
//
// It authorizes the caller against the instance, resolves the verb to a runtime
// target via the template contract, and reverse-proxies to the runtime cluster
// the provider owns. Consumers therefore never hold a runtime credential.
type Handler struct {
	instances InstanceGetter
	contracts ContractGetter
	runtime   Runtime
}

// NewHandler wires the handler. Any nil dependency makes the data plane report
// 503 (the serve process runs without a runtime/kcp config in dev).
func NewHandler(instances InstanceGetter, contracts ContractGetter, runtime Runtime) *Handler {
	return &Handler{instances: instances, contracts: contracts, runtime: runtime}
}

// request is the parsed addressing of a data-plane call.
type request struct {
	workspace  string
	resource   string
	name       string
	verb       string
	callerPath string // remaining path beyond the verb (open-proxy tail)
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if h == nil || h.instances == nil || h.contracts == nil || h.runtime == nil {
		http.Error(w, "data plane unavailable on this provider", http.StatusServiceUnavailable)
		return
	}

	id := identityFromRequest(r)
	if id.token == "" {
		http.Error(w, "no bearer token — cannot act on the caller's behalf", http.StatusUnauthorized)
		return
	}

	req, ok := parsePath(r.URL.Path)
	if !ok {
		http.Error(w, "bad data-plane path; want /dataplane/clusters/<ws>/<resource>/<name>/<verb>", http.StatusBadRequest)
		return
	}

	// 1. Authorize + fetch the instance as the caller. RBAC is the gate.
	instance, err := h.instances.Get(r.Context(), req.workspace, id.token, req.resource, req.name)
	if err != nil {
		writeKubeError(w, err)
		return
	}

	// 2. Resolve the template's data-plane contract for this resource.
	contract, err := h.contracts.For(r.Context(), req.resource)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	if contract == nil {
		http.Error(w, "resource "+req.resource+" exposes no data plane", http.StatusNotFound)
		return
	}

	// 3. Method allowlist for the verb.
	if !MethodAllowed(contract, req.verb, r.Method) {
		http.Error(w, "method "+r.Method+" not allowed for verb "+req.verb, http.StatusMethodNotAllowed)
		return
	}

	// 4. Resolve the verb to a concrete runtime target (namespace-confined).
	target, err := Resolve(contract, instance, req.verb)
	if err != nil {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}

	// 5a. A status verb is served straight from the instance status — no hop.
	if target.FromStatus {
		writeInstanceStatus(w, instance)
		return
	}

	// 5b. Reverse-proxy to the runtime Service the provider owns.
	serveProxy(w, r, h.runtime, target, req.callerPath)
}

// parsePath parses /dataplane/clusters/<ws>/<resource>/<name>/<verb>[/<tail...>].
// The workspace segment may itself contain colons (root:kedge:orgs:acme); it is
// a single path segment, so no escaping is needed.
func parsePath(p string) (request, bool) {
	rest := strings.TrimPrefix(p, PathPrefix)
	if rest == p {
		return request{}, false
	}
	rest = strings.TrimPrefix(rest, "clusters/")
	parts := strings.SplitN(rest, "/", 5)
	// parts: [ws, resource, name, verb, tail?]
	if len(parts) < 4 {
		return request{}, false
	}
	req := request{
		workspace: strings.TrimSpace(parts[0]),
		resource:  strings.TrimSpace(parts[1]),
		name:      strings.TrimSpace(parts[2]),
		verb:      strings.TrimSpace(parts[3]),
	}
	if req.workspace == "" || req.resource == "" || req.name == "" || req.verb == "" {
		return request{}, false
	}
	if len(parts) == 5 {
		req.callerPath = "/" + parts[4]
	}
	return req, true
}

// writeInstanceStatus returns the instance's status subobject as JSON.
func writeInstanceStatus(w http.ResponseWriter, instance *unstructured.Unstructured) {
	status, _, _ := unstructured.NestedMap(instance.Object, "status")
	if status == nil {
		status = map[string]any{}
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(status)
}

// writeKubeError maps a Kubernetes API error from the authorize/fetch step to
// an HTTP status, so a caller's 403/404 surfaces faithfully rather than as 500.
func writeKubeError(w http.ResponseWriter, err error) {
	var statusErr *apierrors.StatusError
	if errors.As(err, &statusErr) {
		code := int(statusErr.ErrStatus.Code)
		if code == 0 {
			code = http.StatusBadGateway
		}
		http.Error(w, statusErr.ErrStatus.Message, code)
		return
	}
	http.Error(w, err.Error(), http.StatusBadGateway)
}
