// Copyright 2026 The Railgrid Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package queryapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/dynamic"
	"k8s.io/klog/v2"

	"github.com/railgrid/kuery/apis/query/v1alpha1"
	"github.com/railgrid/kuery/pkg/engine"

	"github.com/railgrid/provider-sdk/actionwire"
	"github.com/railgrid/provider-sdk/dataplane"

	kueryv1alpha1 "github.com/railgrid/provider-kuery/apis/v1alpha1"
)

// RunVerb is the data-plane verb: the kcp custom subresource savedviews/run
// on kuery's APIExport. kcp authorizes the HTTP method as the RBAC verb on
// that coordinate before it forwards the request here.
const RunVerb = "run"

// RunPath is the one route the verb is reached on — a kube path on whichever
// kcp front door the caller holds a credential for, forwarded by the shard
// with the caller's identity stamped in requestheader headers:
//
//	POST /clusters/{clusterID}/apis/kuery.providers.railgrid.ai/v1alpha1/savedviews/{name}/run
//
// There is no hub-proxied spelling. The grammar is provider-sdk/dataplane's;
// serve's subresource adapter parses it and hands the route to the handler in
// the request context (dataplane.RouteFrom).
const RunPath = "/clusters/{clusterID}/apis/" + kueryv1alpha1.GroupName + "/" + kueryv1alpha1.Version + "/savedviews/{name}/run"

// DefaultLimits bound one query's HTTP shape. The engine has its own caps
// (30s, 10k rows, depth 20); these are the request and response envelope
// around them.
var DefaultLimits = dataplane.Limits{
	Timeout:        45 * time.Second,
	MaxInputBytes:  64 << 10,
	MaxOutputBytes: 8 << 20,
}

// EngagementLister answers which edges a tenant may query. It is the
// Engagement set in kuery's own workspace (engagement.Registry), taken as an
// interface so the request path does not import a controller — and so a test
// can state a fleet in one line.
//
// It is the AUTHORITY for tenant scoping. The kuery store's tenant label is
// how that answer reaches SQL, not where it comes from.
type EngagementLister interface {
	EngagedEdges(ctx context.Context, cluster string) ([]string, error)
}

// RunHandler serves the query verb.
//
// It is the only path from the outside world to the kuery store. On the verb
// route kcp has already authenticated the caller and authorized the HTTP
// method on savedviews/run with ordinary RBAC; what remains is visibility,
// which dataplane.Gate settles with a SubjectAccessReview on the caller's
// behalf before reading the SavedView AS THE PROVIDER through kuery's export
// virtual workspace. There is no caller bearer on this path and the handler
// never acts as the caller: everything after the gate runs with the provider
// client the gate returned. Neither step consults a header — the cluster is
// the one in the path the shard forwarded.
//
// The MCP tools reach the same executor with the caller's own bearer, the one
// class the hub still proxies with a credential (RunSavedView).
type RunHandler struct {
	// Engine is the embedded kuery query engine.
	Engine *engine.Engine
	// Callers acts as the provider through its export virtual workspace on
	// the verb route, and as a bearer-credentialed caller for MCP tools.
	Callers dataplane.ProviderCallerFactory
	// Engagements is the engaged-edge authority.
	Engagements EngagementLister
	// Limits bound the request and response; the zero value uses DefaultLimits.
	Limits dataplane.Limits
}

// runInput is the request body's "input" member. An empty body runs the
// SavedView as saved; a query here overrides it for this call only, which is
// what makes the playground a SavedView run rather than a second, ungated
// route.
type runInput struct {
	Query json.RawMessage `json:"query,omitempty"`
}

func (h *RunHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	logger := klog.FromContext(r.Context())

	route, ok := dataplane.RouteFrom(r.Context())
	if !ok || route.Group != kueryv1alpha1.GroupName ||
		route.Resource != kueryv1alpha1.SavedViewsResource.Resource || route.Verb != RunVerb ||
		route.Component != "" || route.Tail != "" {
		dataplane.WriteError(w, dataplane.ErrBadPath)
		return
	}
	req := route.Request

	view, provider, err := dataplane.Gate(r.Context(), h.Callers, kueryv1alpha1.SavedViewsResource, req)
	if err != nil {
		// The detail names the caller's workspace and the object; it stays here.
		logger.V(2).Info("query verb refused", "cluster", req.ClusterID, "view", req.Name, "err", err.Error())
		dataplane.WriteError(w, err)
		return
	}
	if identity, ok := dataplane.ProxiedIdentityFrom(r.Context()); ok {
		logger.V(4).Info("query verb", "cluster", req.ClusterID, "view", req.Name, "user", identity.User)
	}

	env := actionwire.New(r, "kuery", RunVerb, actionwire.ResourceRef{
		APIVersion: kueryv1alpha1.SchemeGroupVersion.String(),
		Kind:       "SavedView",
		Resource:   kueryv1alpha1.SavedViewsResource.Resource,
		Name:       req.Name,
	})
	dataplane.Serve(w, r, env, h.limits(), func(ctx context.Context, input json.RawMessage) (any, *actionwire.Error) {
		raw, aerr := queryDocument(view, input)
		if aerr != nil {
			return nil, aerr
		}
		return h.execute(ctx, provider, view, req.ClusterID, raw)
	})
}

func (h *RunHandler) limits() dataplane.Limits {
	if h.Limits == (dataplane.Limits{}) {
		return DefaultLimits
	}
	return h.Limits
}

// execute runs one already-gated query. view is what the gate returned, and
// client is whoever the gate ran as: the provider on the verb route, the
// caller on the MCP path. It is used for nothing but the best-effort
// lastOpenedAt stamp; the query itself runs against the store, scoped to the
// caller's workspace by the path cluster and its Engagement records.
func (h *RunHandler) execute(
	ctx context.Context,
	client dynamic.Interface,
	view *unstructured.Unstructured,
	cluster string,
	raw json.RawMessage,
) (*v1alpha1.QueryStatus, *actionwire.Error) {
	// Validate before parsing: the reconciler stamps Ready from the same
	// check, but a view saved by an older build — or an override typed into
	// the playground a second ago — has never been through it.
	if err := ValidateQuerySpec(raw); err != nil {
		return nil, &actionwire.Error{Code: "invalid_query", Message: err.Error()}
	}
	spec := &v1alpha1.QuerySpec{}
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, spec); err != nil {
			return nil, &actionwire.Error{Code: "invalid_query", Message: "query is not a kuery QuerySpec: " + err.Error()}
		}
	}

	edges, err := h.engagedEdges(ctx, cluster)
	if err != nil {
		klog.FromContext(ctx).Error(err, "listing engagements for a query", "cluster", cluster)
		return nil, &actionwire.Error{Code: "engagements_unavailable", Message: "the engaged-edge set could not be read", Retryable: true}
	}
	if err := ScopeToTenant(spec, cluster, edges); err != nil {
		// Both scoping failures are about the caller's own fleet and name
		// nothing outside it, so they are safe to return.
		return nil, &actionwire.Error{Code: "not_engaged", Message: err.Error()}
	}

	status, err := h.Engine.Execute(ctx, spec)
	if err != nil {
		// The engine prefixes caller-controlled validation failures with
		// "validation:" and wraps everything else (SQL generation, execution,
		// scanning) under its own prefixes. Validation messages are safe and
		// actionable; internal store failures leak driver internals the user
		// cannot act on, so those are logged and reported generically.
		if strings.HasPrefix(err.Error(), "validation:") {
			return nil, &actionwire.Error{Code: "invalid_query", Message: err.Error()}
		}
		klog.FromContext(ctx).Error(err, "kuery query execution failed", "cluster", cluster)
		return nil, &actionwire.Error{Code: "query_failed", Message: "the query could not be executed", Retryable: true}
	}

	h.stampLastOpened(ctx, client, view)
	return status, nil
}

// queryDocument picks the query to run: the body's override when it carries
// one, the SavedView's own spec.query otherwise.
func queryDocument(view *unstructured.Unstructured, input json.RawMessage) (json.RawMessage, *actionwire.Error) {
	if len(input) > 0 && string(input) != "null" {
		var parsed runInput
		decoder := json.NewDecoder(strings.NewReader(string(input)))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&parsed); err != nil {
			return nil, &actionwire.Error{Code: "invalid_input", Message: `input must be {"query": <QuerySpec>} or empty`}
		}
		if len(parsed.Query) > 0 && string(parsed.Query) != "null" {
			return parsed.Query, nil
		}
	}
	return savedQuery(view), nil
}

// savedQuery is the view's own spec.query, or nil when it has none — "the
// whole fleet", which scoping still narrows to the caller's engaged edges.
func savedQuery(view *unstructured.Unstructured) json.RawMessage {
	saved, found, err := unstructured.NestedFieldNoCopy(view.Object, "spec", "query")
	if err != nil || !found || saved == nil {
		return nil
	}
	encoded, err := json.Marshal(saved)
	if err != nil {
		return nil
	}
	return encoded
}

// RunSavedView runs a query for a caller that did not arrive on the verb
// route — the MCP tools — through gates of the same meaning.
//
// MCP is the one class the hub still proxies with the caller's own bearer, so
// here the caller is a credential rather than a stamped identity, and both
// gates run AS THE CALLER: a real GET of the addressed SavedView proves the
// caller is in that workspace and may see the view, and a
// SelfSubjectAccessReview for create on savedviews/run proves they were
// granted the verb kcp would otherwise have authorized on the kube path.
//
// The MCP transport has no data-plane path, so the workspace comes from the
// X-Railgrid-Cluster header the aggregate's federation client injects. That is
// addressing, not authorization: the caller client is built from the caller's
// own bearer against that cluster, so a forged header buys a client whose GET
// and whose access review both fail.
//
// viewName empty means the caller's own scratch view, created on first use.
// query empty means run the named view as saved.
func (h *RunHandler) RunSavedView(
	ctx context.Context,
	r *http.Request,
	viewName string,
	query json.RawMessage,
) (*v1alpha1.QueryStatus, error) {
	bearer, cluster, user, err := dataplane.Identity(r)
	if err != nil {
		return nil, err
	}
	if !dataplane.IsClusterID(cluster) {
		return nil, fmt.Errorf("%w: no usable %s on the request", dataplane.ErrBadPath, dataplane.HeaderCluster)
	}
	if h.Callers == nil {
		return nil, fmt.Errorf("no caller factory configured")
	}
	caller, err := h.Callers.For(cluster, bearer)
	if err != nil {
		return nil, err
	}

	scratch := strings.TrimSpace(viewName) == ""
	if scratch {
		viewName = PlaygroundViewName(user)
		if err := EnsurePlaygroundView(ctx, caller, viewName, user); err != nil {
			return nil, err
		}
	}

	view, err := gateAsCaller(ctx, caller, viewName)
	if err != nil {
		return nil, err
	}
	if len(query) == 0 {
		query = savedQuery(view)
	}
	status, aerr := h.execute(ctx, caller, view, cluster, query)
	if aerr != nil {
		return nil, fmt.Errorf("%s: %s", aerr.Code, aerr.Message)
	}
	return status, nil
}

// gateAsCaller is the MCP path's gate, run with the caller's own credential:
// the SavedView must be readable by the caller in the addressed workspace and
// not on its way out, and the caller must hold create on savedviews/run for
// that name — the rule the hub materializes every data-plane grant as
// (dataplane.SSARVerb). Every probe-shaped failure collapses to ErrDenied so
// a tool call cannot learn whether a view exists from the answer.
func gateAsCaller(ctx context.Context, caller dynamic.Interface, viewName string) (*unstructured.Unstructured, error) {
	views := kueryv1alpha1.SavedViewsResource
	view, err := caller.Resource(views).Get(ctx, viewName, metav1.GetOptions{})
	if err != nil {
		if apierrors.IsNotFound(err) || apierrors.IsForbidden(err) || apierrors.IsUnauthorized(err) {
			return nil, fmt.Errorf("%w: %s/%s: %w", dataplane.ErrDenied, views.Resource, viewName, err)
		}
		return nil, fmt.Errorf("reading %s/%s as the caller: %w", views.Resource, viewName, err)
	}
	if view.GetDeletionTimestamp() != nil {
		return nil, fmt.Errorf("%w: %s/%s is being deleted", dataplane.ErrDenied, views.Resource, viewName)
	}

	review := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "authorization.k8s.io/v1",
		"kind":       "SelfSubjectAccessReview",
		"spec": map[string]any{
			"resourceAttributes": map[string]any{
				"group":       views.Group,
				"version":     views.Version,
				"resource":    views.Resource,
				"subresource": RunVerb,
				"name":        viewName,
				"verb":        dataplane.SSARVerb,
			},
		},
	}}
	answered, err := caller.Resource(dataplane.SelfSubjectAccessReviews()).Create(ctx, review, metav1.CreateOptions{})
	if err != nil {
		return nil, fmt.Errorf("access review for %s on %s/%s: %w", RunVerb, views.Resource, viewName, err)
	}
	allowed, _, err := unstructured.NestedBool(answered.Object, "status", "allowed")
	if err != nil {
		return nil, fmt.Errorf("access review has no status.allowed: %w", err)
	}
	if !allowed {
		return nil, fmt.Errorf("%w: caller is not granted %s on %s/%s", dataplane.ErrDenied, RunVerb, views.Resource, viewName)
	}
	return view, nil
}

func (h *RunHandler) engagedEdges(ctx context.Context, cluster string) ([]string, error) {
	if h.Engagements == nil {
		return nil, fmt.Errorf("no engagement lister configured")
	}
	return h.Engagements.EngagedEdges(ctx, cluster)
}

// stampLastOpened records the run on the view — as the provider on the verb
// route, as the caller on the MCP path. Best effort: a query that ran is a
// query that succeeded, and losing the timestamp is a cosmetic loss in the
// portal's "recently used" ordering.
func (h *RunHandler) stampLastOpened(ctx context.Context, client dynamic.Interface, view *unstructured.Unstructured) {
	patch := fmt.Sprintf(`{"status":{"lastOpenedAt":%q}}`, metav1.Now().UTC().Format(time.RFC3339))
	_, err := client.Resource(kueryv1alpha1.SavedViewsResource).
		Patch(ctx, view.GetName(), types.MergePatchType, []byte(patch), metav1.PatchOptions{}, "status")
	if err != nil && !apierrors.IsNotFound(err) && !apierrors.IsForbidden(err) {
		klog.FromContext(ctx).V(4).Info("could not stamp status.lastOpenedAt", "view", view.GetName(), "err", err.Error())
	}
}
