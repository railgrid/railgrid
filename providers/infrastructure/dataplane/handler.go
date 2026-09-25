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

package dataplane

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"path"
	"strings"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/klog/v2"

	infrav1alpha1 "github.com/railgrid/provider-infrastructure/apis/v1alpha1"
	"github.com/railgrid/provider-infrastructure/kro"
	sdk "github.com/railgrid/provider-sdk/dataplane"
)

// instancesGVR is the only resource this data plane serves. The flattened API
// has a single tenant-facing kind, so every verb — instance-level and
// component-level alike — is a kcp custom subresource "instances/{verb}" of
// it: one grant covers .../instances/{name}/{verb} with and without
// ?component=, because a component is an addressing detail of the same object
// and not a separate thing to grant.
var instancesGVR = schema.GroupVersionResource{
	Group:    "infrastructure.railgrid.ai",
	Version:  "v1alpha1",
	Resource: infrav1alpha1.InstancesResource,
}

// Handler serves a template's declared data-plane verbs as kcp custom
// subresources on a workload instance, reached on the kcp front door as
//
//	/clusters/<id>/apis/infrastructure.railgrid.ai/v1alpha1/instances/<name>/<verb>[/<caller-path...>][?component=<c>]
//
// e.g. /clusters/rgl3jcl2cfl3xa5p/apis/infrastructure.railgrid.ai/v1alpha1/instances/my-site-dev/log?component=app
//
// kcp authenticates the caller, authorizes the verb with ordinary RBAC and
// reverse-proxies the request here with the caller's identity stamped in
// requestheader headers. serve's subresource adapter has already parsed the
// path (every refusal the contract requires) and put the route and the caller
// in the request context; the grammar and the gate come from
// provider-sdk/dataplane. Gate decides the caller's visibility of the Instance
// with a SubjectAccessReview on their behalf and reads the object as the
// provider. What stays here is what only this provider knows: resolving the
// verb against the template contract the *authorized* Instance names,
// confining it to the runtime namespace that Instance owns, and
// reverse-proxying to the runtime cluster. Consumers therefore never hold a
// runtime credential, and no caller credential ever reaches this handler.
type Handler struct {
	callers     sdk.ProviderCallerFactory
	contracts   ContractGetter
	development DevelopmentGetter
	runtime     Runtime
	executor    Executor
}

// ActivityRecorder persists an accepted data-plane call on the runtime object
// that backs an Instance. It is optional so existing in-memory/test Runtime
// implementations remain valid; the production runtime implements it with a
// provider-owned status.runtimeRef patch.
type ActivityRecorder interface {
	RecordActivity(context.Context, *unstructured.Unstructured) error
}

// HandlerOption configures optional data-plane capabilities while preserving
// the original three-argument NewHandler call sites.
type HandlerOption func(*Handler)

// WithExec wires the bounded persistent command executor. Authorization is
// not a separate hook any more: exec is gated exactly like every other verb,
// by the caller's SSAR for `create` on instances/exec.
func WithExec(executor Executor) HandlerOption {
	return func(h *Handler) { h.executor = executor }
}

// WithDevelopmentGetter supplies the platform-owned development metadata used
// to derive the component workspace path and absolute working directory.
func WithDevelopmentGetter(getter DevelopmentGetter) HandlerOption {
	return func(h *Handler) { h.development = getter }
}

// NewHandler wires the handler. Any nil dependency makes the data plane report
// 503 (the serve process runs without a runtime cluster in dev). callers must
// be able to act as the provider through its export virtual workspace: with
// no caller bearer on a verb, that is the only client the gate has. If the
// contract getter also implements DevelopmentGetter it is used automatically
// for exec calls.
func NewHandler(callers sdk.ProviderCallerFactory, contracts ContractGetter, runtime Runtime, options ...HandlerOption) *Handler {
	h := &Handler{callers: callers, contracts: contracts, runtime: runtime}
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

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if h == nil || h.callers == nil || h.contracts == nil || h.runtime == nil {
		http.Error(w, "data plane unavailable on this provider", http.StatusServiceUnavailable)
		return
	}
	logger := klog.FromContext(r.Context()).WithName("infrastructure-dataplane")

	// The route is what serve's subresource adapter parsed; a request that
	// carries none did not come through the adapter and nothing else is
	// entitled to say what it addresses.
	route, ok := sdk.RouteFrom(r.Context())
	if !ok {
		sdk.WriteError(w, sdk.ErrBadPath)
		return
	}
	req := route.Request
	// The flattened API serves exactly one instance resource; anything else
	// is an address from the retired per-template era. Answer it the way a
	// denied request is answered, so the route set is not enumerable.
	if route.Group != instancesGVR.Group || req.Resource != instancesGVR.Resource {
		sdk.WriteError(w, sdk.ErrDenied)
		return
	}

	// 1. The gate: the caller kcp stamped must be able to see the Instance
	// (a SubjectAccessReview on their behalf), which is then read as the
	// provider — the authoritative object everything below is resolved from.
	// The verb grant itself is not repeated: kcp authorized instances/{verb}
	// with ordinary RBAC before it forwarded the request, and a component
	// verb collapses onto the same subresource.
	instance, _, err := sdk.Gate(r.Context(), h.callers, instancesGVR, req)
	if err != nil {
		// The detail stays provider-side; the caller learns only the status.
		logger.V(3).Info("data-plane request refused", "verb", req.Verb, "component", req.Component, "instance", req.Name, "err", err)
		sdk.WriteError(w, err)
		return
	}
	// Gate has already accepted the identity, so it is present. It is a
	// label (who started an exec session), never a credential.
	caller, _ := sdk.ProxiedIdentityFrom(r.Context())

	// 2. Resolve the data-plane contract of the instance's template. The
	// template name comes from the instance the caller was just authorized
	// on — never from a request field.
	templateName, _, _ := unstructured.NestedString(instance.Object, "spec", "template")
	contract, err := h.contracts.For(r.Context(), templateName)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	if contract == nil {
		http.Error(w, "template "+templateName+" exposes no data plane", http.StatusNotFound)
		return
	}
	expectedRuntimeNamespace := kro.RuntimeNamespace(req.ClusterID, instance.GetNamespace())
	if err := ValidateRuntimeNamespace(contract, instance, expectedRuntimeNamespace); err != nil {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}

	// Exec is a typed, fixed POST capability. It never falls through to the
	// generic endpoint proxy, even if a template happens to declare an endpoint
	// with the same verb or Upgrade=true.
	if req.Verb == "exec" {
		h.serveExec(w, r, caller, req, templateName, contract, instance)
		return
	}

	// 3+4. Method allowlist, then resolve the verb to a concrete runtime
	// target (namespace-confined). Component verbs differ only in lookup.
	var target ResolvedTarget
	if req.Component != "" {
		if !ComponentMethodAllowed(contract, req.Component, req.Verb, r.Method) {
			http.Error(w, "method "+r.Method+" not allowed for verb "+req.Component+"/"+req.Verb, http.StatusMethodNotAllowed)
			return
		}
		target, err = ResolveComponent(contract, instance, req.Component, req.Verb)
	} else {
		if !MethodAllowed(contract, req.Verb, r.Method) {
			http.Error(w, "method "+r.Method+" not allowed for verb "+req.Verb, http.StatusMethodNotAllowed)
			return
		}
		target, err = Resolve(contract, instance, req.Verb)
	}
	if err != nil {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	if err := h.recordActivity(r.Context(), instance); err != nil {
		http.Error(w, "activity marker unavailable: "+err.Error(), http.StatusServiceUnavailable)
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
	// hop only needs the runtime Service. The tail is what the caller
	// addressed beneath the verb (the proxy verb's upstream path).
	serveProxy(w, r, h.runtime, target, req.Tail)
}

// verbQuery is the part of the query string that belongs to the verb:
// everything the caller sent except the component parameter, which addresses
// the object and has already been parsed onto the route.
func verbQuery(r *http.Request) url.Values {
	q := r.URL.Query()
	q.Del(sdk.ComponentQuery)
	return q
}

func (h *Handler) serveExec(w http.ResponseWriter, r *http.Request, caller sdk.ProxiedIdentity, req sdk.Request, templateName string, contract *infrav1alpha1.TemplateDataPlane, instance *unstructured.Unstructured) {
	if req.Component == "" {
		http.Error(w, "exec is only available for a declared component", http.StatusNotFound)
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "exec requires POST", http.StatusMethodNotAllowed)
		return
	}
	if req.Tail != "" || len(verbQuery(r)) != 0 {
		http.Error(w, "exec does not accept a caller path or query", http.StatusBadRequest)
		return
	}
	component, ok := contract.Components[req.Component]
	if !ok || component.Exec == nil {
		http.Error(w, "exec is not declared for component "+req.Component, http.StatusNotFound)
		return
	}
	if h.executor == nil {
		http.Error(w, "exec is unavailable on this provider", http.StatusServiceUnavailable)
		return
	}
	if !instanceReadyForExec(instance, templateName) {
		http.Error(w, "exec is unavailable until the Instance is Ready and network phase is runtime", http.StatusConflict)
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
	dev, err := h.development.DevelopmentFor(r.Context(), templateName, req.Component)
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
	controlTarget, err := ResolveComponentExecTarget(contract, instance, req.Component)
	if err != nil {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	if err := h.recordActivity(r.Context(), instance); err != nil {
		http.Error(w, "activity marker unavailable: "+err.Error(), http.StatusServiceUnavailable)
		return
	}
	launches := reqBody.Action == ExecActionStart || reqBody.Action == ExecActionRun
	if launches && reqBody.SourceRevision == 0 && reqBody.SourceDigest == "" {
		// Default to the revision the component has applied. It is resolved
		// only after the gates so an unauthorized caller cannot make the
		// provider reach the runtime.
		statusTarget, err := ResolveComponentStatusTarget(contract, instance, req.Component)
		if err != nil {
			http.Error(w, err.Error(), http.StatusConflict)
			return
		}
		revision, digest, err := fetchComponentSource(r.Context(), h.runtime, statusTarget)
		if errors.Is(err, errNoAppliedSource) {
			http.Error(w, fmt.Sprintf("sourceRevision is required for %s: component %q reports no applied source revision — sync its workspace first (dev_sync, or POST .../instances/<name>/sync?component=%s), wait for any dependency reload to finish, then retry; or pass sourceRevision and sourceDigest explicitly", reqBody.Action, req.Component, req.Component), http.StatusBadRequest)
			return
		}
		if err != nil {
			http.Error(w, "resolve the component's applied source revision: "+err.Error(), http.StatusBadGateway)
			return
		}
		reqBody.SourceRevision, reqBody.SourceDigest = revision, digest
	}
	call := ExecCall{
		Workspace:        req.ClusterID,
		Resource:         req.Resource,
		Name:             req.Name,
		Component:        req.Component,
		Instance:         instance,
		Capability:       component.Exec,
		WorkingDir:       workingDir,
		WorkspacePath:    strings.TrimSpace(dev.WorkspacePath),
		CallerKey:        execCallerKey(caller),
		RuntimeNamespace: runtimeNamespace,
		ControlTarget:    controlTarget,
		Request:          reqBody,
		IdempotencyKey:   idempotencyKey,
	}
	var result ExecResult
	switch reqBody.Action {
	case ExecActionStart:
		result, err = h.executor.Start(r.Context(), call)
	case ExecActionRun:
		result, err = runExec(r.Context(), h.executor, call)
	case ExecActionPoll:
		result, err = h.executor.Poll(r.Context(), call)
	case ExecActionCancel:
		result, err = h.executor.Cancel(r.Context(), call)
	}
	if err != nil {
		writeExecError(w, err)
		return
	}
	if launches {
		result.SourceRevision, result.SourceDigest = reqBody.SourceRevision, reqBody.SourceDigest
		if result.RequestID == "" {
			result.RequestID = reqBody.RequestID
		}
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

// instanceReadyForExec is deliberately fail-closed. The controller stamps the
// platform-owned network phase while the runtime is being bootstrapped and
// mirrors the runtime's readiness onto the Instance. Exec may only reach the
// dev-agent after both signals say that the live runtime is ready.
func instanceReadyForExec(instance *unstructured.Unstructured, templateName string) bool {
	if instance == nil {
		return false
	}
	generation := instance.GetGeneration()
	if generation <= 0 {
		return false
	}
	statusObserved, found, err := unstructured.NestedFieldNoCopy(instance.Object, "status", "observedGeneration")
	if err != nil || !found {
		return false
	}
	observedGeneration, ok := observedGenerationValue(statusObserved)
	if !ok || observedGeneration != generation {
		return false
	}
	phase, found, err := unstructured.NestedString(instance.Object, "status", "phase")
	if err != nil || !found || phase != "Ready" {
		return false
	}
	conditions, found, err := unstructured.NestedSlice(instance.Object, "status", "conditions")
	if err != nil || !found {
		return false
	}
	ready := false
	for _, raw := range conditions {
		condition, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		if conditionType, _ := condition["type"].(string); conditionType != "Ready" {
			continue
		}
		status, _ := condition["status"].(string)
		conditionObserved, ok := observedGenerationValue(condition["observedGeneration"])
		if !ok || conditionObserved != generation {
			return false
		}
		ready = status == "True"
		break
	}
	if !ready {
		return false
	}
	// The platform-owned universal sandbox has a status phase gate that must
	// be present and runtime. Ordinary development templates predate the
	// universal network-phase contract; when their controller has not yet
	// mirrored a phase, preserve their existing Ready-based exec behavior.
	rawPhase, found, err := unstructured.NestedFieldNoCopy(instance.Object, "status", infrav1alpha1.RailgridNetworkPhaseStatusField)
	if err != nil {
		return false
	}
	if templateName == infrav1alpha1.UniversalCodingSandboxTemplateName {
		if !found {
			return false
		}
		networkPhase, ok := rawPhase.(string)
		return ok && networkPhase == infrav1alpha1.RailgridNetworkPhaseRuntime
	}
	return true
}

func observedGenerationValue(value any) (int64, bool) {
	switch value := value.(type) {
	case int64:
		return value, true
	case int32:
		return int64(value), true
	case int:
		return int64(value), true
	case uint64:
		if value > uint64(^uint64(0)>>1) {
			return 0, false
		}
		return int64(value), true
	case uint32:
		return int64(value), true
	case uint:
		return int64(value), true
	case float64:
		if value != float64(int64(value)) {
			return 0, false
		}
		return int64(value), true
	default:
		return 0, false
	}
}

func (h *Handler) recordActivity(ctx context.Context, instance *unstructured.Unstructured) error {
	recorder, ok := h.runtime.(ActivityRecorder)
	if !ok {
		return nil
	}
	return recorder.RecordActivity(ctx, instance)
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
