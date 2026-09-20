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

// RunVerb is the data-plane verb, and therefore the SSAR subresource the hub
// materializes a grant for: create on savedviews/run.
const RunVerb = "run"

// RunPathPrefix is the route main mounts. The grammar underneath it is
// provider-sdk/dataplane's, not kuery's:
//
//	POST /dataplane/clusters/{clusterID}/savedviews/{name}/run
const RunPathPrefix = "/" + dataplane.DataplaneRoot + "/clusters/"

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
// It is the only path from the outside world to the kuery store, and every
// request through it is authorized twice as the caller before the engine is
// touched: a GET of the addressed SavedView in the path's cluster proves the
// caller is in that workspace and may see the view, and a
// SelfSubjectAccessReview for create on savedviews/run proves they were
// granted the verb on that name. Neither gate consults a header, so a forged
// X-Railgrid-Cluster sent straight at the pod changes nothing — the path
// cluster is what is gated, and a mismatch between the two is refused outright.
type RunHandler struct {
	// Engine is the embedded kuery query engine.
	Engine *engine.Engine
	// Callers builds the caller-scoped client both gates run through.
	Callers dataplane.CallerFactory
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

	req, ok := dataplane.ParseRequest(dataplane.DataplaneRoot, r)
	if !ok || req.Resource != kueryv1alpha1.SavedViewsResource.Resource || req.Verb != RunVerb ||
		req.Component != "" || req.Tail != "" {
		dataplane.WriteError(w, dataplane.ErrBadPath)
		return
	}

	view, caller, err := dataplane.Gate(r.Context(), r, h.Callers, kueryv1alpha1.SavedViewsResource, req)
	if err != nil {
		// The detail names the caller's workspace and the object; it stays here.
		logger.V(2).Info("query verb refused", "cluster", req.ClusterID, "view", req.Name, "err", err.Error())
		dataplane.WriteError(w, err)
		return
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
		return h.execute(ctx, caller, view, req.ClusterID, raw)
	})
}

func (h *RunHandler) limits() dataplane.Limits {
	if h.Limits == (dataplane.Limits{}) {
		return DefaultLimits
	}
	return h.Limits
}

// execute runs one already-gated query. view is what gate 1 returned — the
// caller's own SavedView, read with the caller's own credential — so nothing
// here is read with the provider's identity.
func (h *RunHandler) execute(
	ctx context.Context,
	caller dynamic.Interface,
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

	h.stampLastOpened(ctx, caller, view)
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

// RunSavedView runs a query for a caller that did not arrive on the REST
// route — the MCP tools — through exactly the same two gates.
//
// The MCP transport has no data-plane path, so the workspace comes from the
// X-Railgrid-Cluster header the aggregate's federation client injects. That is
// addressing, not authorization: the caller client is built from the caller's
// own bearer against that cluster, and both gates then run against it, so a
// forged header buys a client whose GET and whose access review both fail. The
// one thing it cannot do is disagree with a path, because there is none.
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

	req := dataplane.Request{
		ClusterID: cluster,
		Resource:  kueryv1alpha1.SavedViewsResource.Resource,
		Name:      viewName,
		Verb:      RunVerb,
	}
	view, caller, err := dataplane.Gate(ctx, r, h.Callers, kueryv1alpha1.SavedViewsResource, req)
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

func (h *RunHandler) engagedEdges(ctx context.Context, cluster string) ([]string, error) {
	if h.Engagements == nil {
		return nil, fmt.Errorf("no engagement lister configured")
	}
	return h.Engagements.EngagedEdges(ctx, cluster)
}

// stampLastOpened records the run on the view, as the caller. Best effort: a
// query that ran is a query that succeeded, and losing the timestamp is a
// cosmetic loss in the portal's "recently used" ordering.
func (h *RunHandler) stampLastOpened(ctx context.Context, caller dynamic.Interface, view *unstructured.Unstructured) {
	patch := fmt.Sprintf(`{"status":{"lastOpenedAt":%q}}`, metav1.Now().UTC().Format(time.RFC3339))
	_, err := caller.Resource(kueryv1alpha1.SavedViewsResource).
		Patch(ctx, view.GetName(), types.MergePatchType, []byte(patch), metav1.PatchOptions{}, "status")
	if err != nil && !apierrors.IsNotFound(err) && !apierrors.IsForbidden(err) {
		klog.FromContext(ctx).V(4).Info("could not stamp status.lastOpenedAt", "view", view.GetName(), "err", err.Error())
	}
}
