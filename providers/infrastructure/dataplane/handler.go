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
	"fmt"
	"net/http"
	"path"
	"strings"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	infrav1alpha1 "github.com/faroshq/provider-infrastructure/apis/v1alpha1"
	"github.com/faroshq/provider-infrastructure/kro"
)

// PathPrefix is where the handler is mounted on the provider's serve mux. It is
// reached through the hub backend proxy at
// /services/providers/infrastructure/dataplane/... with the caller's bearer
// token forwarded as-is and X-Faros-* identity headers injected.
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
// e.g. /dataplane/clusters/rgl3jcl2cfl3xa5p/simplewebapps/my-site-dev/components/app/log
//
// It authorizes the caller against the instance, resolves the verb to a runtime
// target via the template contract, and reverse-proxies to the runtime cluster
// the provider owns. Consumers therefore never hold a runtime credential.
type Handler struct {
	instances   InstanceGetter
	contracts   ContractGetter
	development DevelopmentGetter
	runtime     Runtime
	executor    Executor
	authorizer  ExecAuthorizer
}

// HandlerOption configures optional data-plane capabilities while preserving
// the original three-argument NewHandler call sites.
type HandlerOption func(*Handler)

// WithExec wires the bounded persistent command executor and its explicit policy hook.
// Both must be supplied for the /exec route to become available.
func WithExec(executor Executor, authorizer ExecAuthorizer) HandlerOption {
	return func(h *Handler) {
		h.executor = executor
		h.authorizer = authorizer
	}
}

// WithDevelopmentGetter supplies the platform-owned development metadata used
// to derive the component workspace path and absolute working directory.
func WithDevelopmentGetter(getter DevelopmentGetter) HandlerOption {
	return func(h *Handler) { h.development = getter }
}

// NewHandler wires the handler. Any nil dependency makes the data plane report
// 503 (the serve process runs without a runtime/kcp config in dev). If the
// contract getter also implements DevelopmentGetter it is used automatically
// for exec calls.
func NewHandler(instances InstanceGetter, contracts ContractGetter, runtime Runtime, options ...HandlerOption) *Handler {
	h := &Handler{instances: instances, contracts: contracts, runtime: runtime}
	if getter, ok := contracts.(DevelopmentGetter); ok {
		h.development = getter
	}
	for _, option := range options {
		if option != nil {
			option(h)
		}
	}
	return h
}

// request is the parsed addressing of a data-plane call.
type request struct {
	workspace  string
	resource   string
	name       string
	component  string // empty for instance-level verbs
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
	expectedRuntimeNamespace := kro.RuntimeNamespace(req.workspace, instance.GetNamespace())
	if err := ValidateRuntimeNamespace(contract, instance, expectedRuntimeNamespace); err != nil {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}

	// Exec is a typed, fixed POST capability. It never falls through to the
	// generic endpoint proxy, even if a template happens to declare an endpoint
	// with the same verb or Upgrade=true.
	if req.verb == "exec" {
		h.serveExec(w, r, id, req, contract, instance)
		return
	}

	// 3+4. Method allowlist, then resolve the verb to a concrete runtime
	// target (namespace-confined). Component verbs differ only in lookup.
	var target ResolvedTarget
	if req.component != "" {
		if !ComponentMethodAllowed(contract, req.component, req.verb, r.Method) {
			http.Error(w, "method "+r.Method+" not allowed for verb "+req.component+"/"+req.verb, http.StatusMethodNotAllowed)
			return
		}
		target, err = ResolveComponent(contract, instance, req.component, req.verb)
	} else {
		if !MethodAllowed(contract, req.verb, r.Method) {
			http.Error(w, "method "+r.Method+" not allowed for verb "+req.verb, http.StatusMethodNotAllowed)
			return
		}
		target, err = Resolve(contract, instance, req.verb)
	}
	if err != nil {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}

	// 5a. A status verb is served straight from the instance status — no hop.
	if target.FromStatus {
		writeInstanceStatus(w, instance)
		return
	}

	// 5b. Reverse-proxy to the runtime Service the provider owns. The public
	// preview HTTPRoute is created declaratively by the template's RGD, so
	// there is no per-request route reconciliation gate here — this internal
	// hop only needs the runtime Service.
	serveProxy(w, r, h.runtime, target, req.callerPath)
}

func (h *Handler) serveExec(w http.ResponseWriter, r *http.Request, id identity, req request, contract *infrav1alpha1.TemplateDataPlane, instance *unstructured.Unstructured) {
	if req.component == "" {
		http.Error(w, "exec is only available for a declared component", http.StatusNotFound)
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "exec requires POST", http.StatusMethodNotAllowed)
		return
	}
	if req.callerPath != "" || r.URL.RawQuery != "" {
		http.Error(w, "exec does not accept a caller path or query", http.StatusBadRequest)
		return
	}
	component, ok := contract.Components[req.component]
	if !ok || component.Exec == nil {
		http.Error(w, "exec is not declared for component "+req.component, http.StatusNotFound)
		return
	}
	if h.executor == nil || h.authorizer == nil {
		http.Error(w, "exec is unavailable on this provider", http.StatusServiceUnavailable)
		return
	}
	reqBody, idempotencyKey, err := decodeExecRequest(w, r, component.Exec)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if h.development == nil {
		http.Error(w, "exec development contract is unavailable", http.StatusServiceUnavailable)
		return
	}
	dev, err := h.development.DevelopmentFor(r.Context(), req.resource, req.component)
	if err != nil {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	workingDir := strings.TrimSpace(dev.WorkingDir)
	if workingDir == "" {
		workingDir = "/workspace"
	}
	if !strings.HasPrefix(workingDir, "/") || strings.Contains(workingDir, "\x00") {
		http.Error(w, "development workingDir must be absolute", http.StatusConflict)
		return
	}
	workingDir = path.Clean(workingDir)
	if resolved, err := resolveExecWorkdir(reqBody.Workdir, workingDir); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	} else {
		// The executor receives the component-relative workdir. WorkingDir on
		// ExecCall remains the platform-owned absolute base and is never
		// caller-controlled.
		reqBody.Workdir = resolved
	}
	runtimeNamespace, err := nestedString(instance, contract.RuntimeNamespacePath)
	if err != nil || runtimeNamespace == "" {
		if err == nil {
			err = fmt.Errorf("runtime namespace at %q is empty; instance is not ready", contract.RuntimeNamespacePath)
		}
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	// Exec is routed to the live dev-agent control Service. The endpoint is
	// deliberately not caller-selectable: resolve the platform-required sync
	// endpoint and replace only its upstream path with the fixed /exec operation.
	controlTarget, err := ResolveComponentExecTarget(contract, instance, req.component)
	if err != nil {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	authorization := ExecAuthorization{
		Workspace:   req.workspace,
		Resource:    req.resource,
		Name:        req.name,
		Component:   req.component,
		User:        id.user,
		CallerToken: id.token,
		Action:      reqBody.Action,
		Request:     reqBody,
		Instance:    instance,
	}
	if err := h.authorizer.AuthorizeExec(r.Context(), authorization); err != nil {
		writeExecAuthorizationError(w, err)
		return
	}
	call := ExecCall{
		Workspace:        req.workspace,
		Resource:         req.resource,
		Name:             req.name,
		Component:        req.component,
		Instance:         instance,
		Capability:       component.Exec,
		WorkingDir:       workingDir,
		WorkspacePath:    strings.TrimSpace(dev.WorkspacePath),
		CallerKey:        execCallerKey(id.token),
		RuntimeNamespace: runtimeNamespace,
		ControlTarget:    controlTarget,
		Request:          reqBody,
		IdempotencyKey:   idempotencyKey,
	}
	var result ExecResult
	switch reqBody.Action {
	case ExecActionStart:
		result, err = h.executor.Start(r.Context(), call)
	case ExecActionPoll:
		result, err = h.executor.Poll(r.Context(), call)
	case ExecActionCancel:
		result, err = h.executor.Cancel(r.Context(), call)
	}
	if err != nil {
		writeExecError(w, err)
		return
	}
	limits, err := limitsForCapability(component.Exec)
	if err != nil {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	result = boundExecResult(result, limits.outputBytes)
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(result); err != nil {
		return
	}
}

func writeExecAuthorizationError(w http.ResponseWriter, err error) {
	if apierrors.IsForbidden(err) || apierrors.IsUnauthorized(err) {
		writeKubeError(w, err)
		return
	}
	http.Error(w, err.Error(), http.StatusForbidden)
}

func writeExecError(w http.ResponseWriter, err error) {
	if apierrors.IsNotFound(err) || apierrors.IsForbidden(err) || apierrors.IsConflict(err) {
		writeKubeError(w, err)
		return
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		http.Error(w, err.Error(), http.StatusRequestTimeout)
		return
	}
	http.Error(w, err.Error(), http.StatusBadGateway)
}

// resolveExecWorkdir keeps caller-selected directories beneath the
// platform-owned development component directory. An empty or "." request
// resolves to that directory; absolute paths are never accepted from callers.
func resolveExecWorkdir(requested, base string) (string, error) {
	base = path.Clean(strings.TrimSpace(base))
	if base == "." || !strings.HasPrefix(base, "/") || strings.Contains(base, "\x00") {
		return "", fmt.Errorf("development workingDir must be absolute")
	}
	requested = strings.TrimSpace(requested)
	if requested == "" || requested == "." {
		return ".", nil
	}
	relative, err := normalizeExecPath(requested)
	if err != nil {
		return "", fmt.Errorf("workdir: %w", err)
	}
	return relative, nil
}

// parsePath parses
//
//	/dataplane/clusters/<id>/<resource>/<name>/<verb>[/<tail...>]
//	/dataplane/clusters/<id>/<resource>/<name>/components/<component>/<verb>[/<tail...>]
//
// The cluster segment is the workspace's kcp logical-cluster ID (the hub-injected
// X-Faros-Cluster that app-studio puts in the URL), NOT a workspace path — the
// instance getter addresses kcp by /clusters/<id>, which the hub proxy requires.
// "components" is reserved as a verb name by the second form.
func parsePath(p string) (request, bool) {
	rest := strings.TrimPrefix(p, PathPrefix)
	if rest == p {
		return request{}, false
	}
	var ok bool
	rest, ok = strings.CutPrefix(rest, "clusters/")
	if !ok {
		return request{}, false
	}
	parts := strings.SplitN(rest, "/", 4)
	// parts: [ws, resource, name, verbAndTail]
	if len(parts) < 4 {
		return request{}, false
	}
	req := request{
		workspace: strings.TrimSpace(parts[0]),
		resource:  strings.TrimSpace(parts[1]),
		name:      strings.TrimSpace(parts[2]),
	}
	verbAndTail := parts[3]
	if trimmed, isComponent := strings.CutPrefix(verbAndTail, "components/"); isComponent {
		seg := strings.SplitN(trimmed, "/", 3)
		// seg: [component, verb, tail?]
		if len(seg) < 2 {
			return request{}, false
		}
		req.component = strings.TrimSpace(seg[0])
		req.verb = strings.TrimSpace(seg[1])
		if req.component == "" {
			return request{}, false
		}
		if len(seg) == 3 {
			req.callerPath = "/" + seg[2]
		}
	} else {
		seg := strings.SplitN(verbAndTail, "/", 2)
		req.verb = strings.TrimSpace(seg[0])
		if len(seg) == 2 {
			req.callerPath = "/" + seg[1]
		}
	}
	if req.workspace == "" || req.resource == "" || req.name == "" || req.verb == "" {
		return request{}, false
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
