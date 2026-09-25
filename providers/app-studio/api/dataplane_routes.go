/*
Copyright 2026 The Railgrid Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package api

// The whole tenant-facing surface of this provider, as data-plane verbs.
//
// It used to be ~95 routes under /api/projects/*: CRUD facades over the
// Project CR, Postgres CRUD for conversation threads, file CRUD over the PVC,
// and — mixed in with them — the verbs that were always verbs (promote, sync,
// restart, logs, hydrate, turns). The hub cannot authorize any of that per
// resource, because nothing in the path says which object is being acted on in
// a way RBAC can see (docs/provider-contract-review.md §3.7).
//
// Everything now lives on one grammar — the kcp custom subresource each verb
// is published as on this provider's APIExport:
//
//	/clusters/{id}/apis/ai.railgrid.ai/v1alpha1/projects/{project}/{verb}[/{tail…}]
//	/clusters/{id}/apis/ai.railgrid.ai/v1alpha1/sessions/{thread}/{verb}[/{tail…}]
//	/clusters/{id}/apis/ai.railgrid.ai/v1alpha1/studios/studio/{verb}
//
// A caller addresses it on the hub's kcp front door like any other API path.
// kcp authenticates the caller, authorizes the HTTP method as the RBAC verb on
// {resource}/{verb} (a grant on a verb coordinate is "*"), and reverse-proxies
// the request here with the caller's identity stamped in headers. serve's
// adapter parses the path, refuses an undeclared coordinate and puts the
// route and the caller in the request context; the dispatcher below runs the
// contract's gate — may this caller SEE the addressed object, decided by a
// SubjectAccessReview on their behalf and read as the provider — and hands the
// handler a client acting as the provider. One grant is one verb on one
// object.
//
// The handlers below are the SAME functions the old routes called. What
// changed is how a request reaches them and what had to be true first — which
// is the point: the authorization story is now in one place instead of
// repeated (or forgotten) in ninety-five.

import (
	"context"
	"log"
	"net/http"
	"strings"

	"github.com/gorilla/mux"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"

	aiv1alpha1 "github.com/railgrid/provider-app-studio/apis/ai/v1alpha1"
	"github.com/railgrid/provider-sdk/dataplane"
)

// The three kinds a verb may hang off. A Studio is the workspace singleton
// (one per workspace, named "studio"), which is what gives workspace-wide
// verbs — creating a project, planning one, listing importable repositories —
// a bound resource to be authorized against. Without it those calls would
// have to stay collection routes, which is exactly the shape the contract has
// no class for.
var (
	projectsGVR = schema.GroupVersionResource{Group: aiv1alpha1.GroupName, Version: aiv1alpha1.Version, Resource: "projects"}
	sessionsGVR = schema.GroupVersionResource{Group: aiv1alpha1.GroupName, Version: aiv1alpha1.Version, Resource: "sessions"}
	studiosGVR  = schema.GroupVersionResource{Group: aiv1alpha1.GroupName, Version: aiv1alpha1.Version, Resource: "studios"}
)

func gvrForResource(resource string) (schema.GroupVersionResource, bool) {
	switch resource {
	case projectsGVR.Resource:
		return projectsGVR, true
	case sessionsGVR.Resource:
		return sessionsGVR, true
	case studiosGVR.Resource:
		return studiosGVR, true
	default:
		return schema.GroupVersionResource{}, false
	}
}

// verbRoute is one served verb.
//
// tailVars names the path segments AFTER the verb, in order, and is how a verb
// that addresses something inside the object keeps a single grant: revoking a
// grant on integrations/{i} should not need a grant per integration, so the
// integration is a tail segment rather than part of the verb. The last entry
// may be greedy (see greedyTail), for a skill package name that legitimately
// contains slashes.
type verbRoute struct {
	verb     string
	tailVars []string
	// greedyTail lets the final tail var absorb the remaining segments.
	greedyTail bool
	// handlers is keyed by HTTP method. A verb that reads and writes the same
	// thing keeps one verb — and therefore one grant — with a handler per
	// method, exactly as the old route did. kcp authorizes the method as the
	// RBAC verb on the coordinate, and a grant on a verb is "*", so every
	// method a handler accepts is one the caller was allowed.
	handlers map[string]func(*Server) http.HandlerFunc
}

// allowed lists the verb's methods in a stable order, for the Allow header.
func (v verbRoute) allowed() []string {
	out := make([]string, 0, len(v.handlers))
	for _, method := range []string{http.MethodGet, http.MethodHead, http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete} {
		if _, ok := v.handlers[method]; ok {
			out = append(out, method)
		}
	}
	return out
}

// bindTail maps the parsed tail onto the route's declared variables. A tail
// that does not fit the declaration is a malformed request, not a 404: the
// verb exists, the address does not.
func (v verbRoute) bindTail(tail string) (map[string]string, bool) {
	segments := []string{}
	if tail != "" {
		segments = strings.Split(tail, "/")
	}
	if len(v.tailVars) == 0 {
		return map[string]string{}, len(segments) == 0
	}
	// Tail variables are optional as a group — `integrations` with no tail is
	// the collection verb, `integrations/{i}` acts on one — but a partial
	// prefix is allowed only up to the greedy tail.
	if len(segments) == 0 {
		return map[string]string{}, true
	}
	out := map[string]string{}
	for i, name := range v.tailVars {
		if i >= len(segments) {
			break
		}
		if v.greedyTail && i == len(v.tailVars)-1 {
			out[name] = strings.Join(segments[i:], "/")
			return out, true
		}
		out[name] = segments[i]
	}
	if len(segments) > len(v.tailVars) {
		return nil, false
	}
	return out, true
}

// DataPlane returns the handler serve.New dispatches every declared verb to.
// It is reached only through serve's subresource adapter, which parsed the
// shard-forwarded path and put the route and the stamped caller in the
// request context; the URL arrives UNCHANGED.
func (s *Server) DataPlane() http.Handler {
	return http.HandlerFunc(s.serveDataPlane)
}

func (s *Server) serveDataPlane(w http.ResponseWriter, r *http.Request) {
	route, ok := dataplane.RouteFrom(r.Context())
	if !ok {
		// Not reached through the adapter: nothing else is entitled to say
		// what this request addresses.
		dataplane.WriteError(w, dataplane.ErrBadPath)
		return
	}
	req := route.Request
	gvr, known := gvrForResource(req.Resource)
	verb, served := lookupVerb(req.Resource, req.Verb)
	if !known || !served || route.Group != aiv1alpha1.GroupName || req.Component != "" {
		// The adapter only dispatches declared coordinates, so this is belt
		// and braces. Answered exactly like a denial so the served route set
		// is not enumerable by a caller who holds no grant on any of it.
		dataplane.WriteError(w, dataplane.ErrDenied)
		return
	}
	handler, allowed := verb.handlers[r.Method]
	if !allowed {
		w.Header().Set("Allow", strings.Join(verb.allowed(), ", "))
		http.Error(w, "method not allowed for verb "+req.Verb, http.StatusMethodNotAllowed)
		return
	}
	tailVars, fits := verb.bindTail(req.Tail)
	if !fits {
		dataplane.WriteError(w, dataplane.ErrBadPath)
		return
	}

	// The gate: kcp already authorized the verb; what is settled here is that
	// the caller can SEE the addressed object (a SubjectAccessReview on their
	// behalf, then a read as the provider). The client it returns is this
	// provider acting in the addressed workspace, and it is what every
	// handler acts through from here on.
	object, provider, err := dataplane.Gate(r.Context(), s.callers, gvr, req)
	if err != nil {
		if dataplane.StatusFor(err) == http.StatusInternalServerError {
			log.Printf("app-studio: %s %s/%s in %s: gate: %v", req.Verb, req.Resource, req.Name, req.ClusterID, err)
		}
		dataplane.WriteError(w, err)
		return
	}

	vars := map[string]string{}
	for name, value := range tailVars {
		vars[name] = value
	}
	switch req.Resource {
	case projectsGVR.Resource:
		vars["project"] = req.Name
	case sessionsGVR.Resource:
		// The project a conversation belongs to is read off the Session the
		// caller was just authorized on, never off the URL. That is strictly
		// stronger than the old route, where the project and the thread were
		// two independent path segments a caller could mismatch.
		project, _, _ := unstructured.NestedString(object.Object, "spec", "projectRef")
		thread, _, _ := unstructured.NestedString(object.Object, "spec", "threadID")
		if project == "" || thread == "" {
			dataplane.WriteError(w, dataplane.ErrDenied)
			return
		}
		vars["project"], vars["thread"] = project, thread
	case studiosGVR.Resource:
		vars["studio"] = req.Name
	}
	// The cluster every handler acts in is the one in the PATH, which the
	// gate just ran against — not an addressing header. The header survives
	// as a label; nothing is authorized from it. The provider client rides
	// along so handlers act through the very client the gate read with.
	ctx := context.WithValue(r.Context(), dataPlaneClusterKey{}, req.ClusterID)
	ctx = context.WithValue(ctx, dataPlaneProviderKey{}, provider)
	r = r.WithContext(ctx)
	// gorilla's own way to put variables on a request. The handlers below are
	// unchanged from when a gorilla route matched them, and they read the same
	// names; what differs is that the names now come from a gated object
	// rather than from whatever the caller typed.
	handler(s)(w, mux.SetURLVars(r, vars))
}

// dataPlaneClusterKey carries the gated logical-cluster ID from the dispatcher
// to identityFromRequest. It is a private type so nothing outside this package
// can put a cluster on a request context.
type dataPlaneClusterKey struct{}

// dataPlaneProviderKey carries the provider client the gate returned.
type dataPlaneProviderKey struct{}

// withDataPlaneProvider records a provider client on ctx, for tests that
// drive a handler without the dispatcher.
func withDataPlaneProvider(ctx context.Context, cluster string, provider dynamic.Interface) context.Context {
	ctx = context.WithValue(ctx, dataPlaneClusterKey{}, cluster)
	if provider != nil {
		ctx = context.WithValue(ctx, dataPlaneProviderKey{}, provider)
	}
	return ctx
}

// dataPlaneCluster returns the logical cluster this request addresses.
//
// After the dispatcher it is the gated value off the context. BEFORE the
// dispatcher — the replica-affinity layer runs first, because forwarding has
// to happen before the body is read — it is parsed from the path. Both are
// the path; neither is a header.
func dataPlaneCluster(r *http.Request) string {
	if r == nil {
		return ""
	}
	if cluster, ok := r.Context().Value(dataPlaneClusterKey{}).(string); ok && cluster != "" {
		return cluster
	}
	if route, ok := dataplane.RouteFrom(r.Context()); ok {
		return route.ClusterID
	}
	if route, ok := ParseRequestPath(r); ok {
		return route.ClusterID
	}
	return ""
}

// ParseRequestPath parses a request against the kube grammar. It is exported
// for the replica-affinity layer, which has to know which workspace and
// project a request addresses before it decides where to serve it.
func ParseRequestPath(r *http.Request) (dataplane.SubresourceRequest, bool) {
	route, err := dataplane.ParseSubresourceRequest(r)
	if err != nil || route.Group != aiv1alpha1.GroupName {
		return dataplane.SubresourceRequest{}, false
	}
	return route, true
}

// verbPath renders the kube path of one of this provider's own verbs, for a
// Location header or a link back into the API.
func verbPath(req dataplane.Request) (string, error) {
	return dataplane.SubresourcePath(aiv1alpha1.GroupName, aiv1alpha1.Version, req)
}
