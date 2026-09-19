// Copyright 2026 The Railgrid Authors.
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy at http://www.apache.org/licenses/LICENSE-2.0

package dataplane

import (
	"context"
	"fmt"
	"net/http"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
)

// SelfSubjectAccessReviews is gate 2's resource. It is issued through the
// caller's dynamic client so a CallerFactory only ever has to build one kind
// of client. It is exported because a test double for a CallerFactory has to
// answer exactly this resource.
func SelfSubjectAccessReviews() schema.GroupVersionResource {
	return schema.GroupVersionResource{
		Group: "authorization.k8s.io", Version: "v1", Resource: "selfsubjectaccessreviews",
	}
}

// SSARVerb is the verb gate 2 asks about, on the virtual subresource
// {resource}/{verb}. The hub materializes every data-plane grant as exactly
// this rule (pkg/hub/serviceaccounts/workload_identity.go), so no other verb
// string works for a workload identity.
const SSARVerb = "create"

// Gate runs the contract's two gates for req as the caller and returns the
// addressed object together with the caller client that read it.
//
// Before either gate it refuses a request whose path cluster disagrees with
// the hub-injected cluster header (ErrClusterMismatch): the path is
// authoritative, and a disagreement means the request was assembled wrong or
// tampered with.
//
//	Gate 1 is a real GET of gvr/req.Name in req.ClusterID as the caller. It
//	proves the caller can see the object and yields the object itself, so the
//	handler can pin a UID or a spec against what it later reads with its own
//	identity. An object with a deletionTimestamp is denied: a verb must not
//	run against something already on its way out.
//
//	Gate 2 is a SelfSubjectAccessReview for "create" on the virtual
//	subresource {gvr.Resource}/{req.Verb}, scoped to req.Name. For an action
//	route req.Verb is the action name without its version, because the grant
//	is per action, not per contract revision.
//
// Every failure a caller could use to probe — no such object, no visibility,
// no grant — comes back as ErrDenied, which WriteError answers with 404.
// Returned errors wrap the sentinels with detail meant for the provider's
// log, never for the response body.
func Gate(
	ctx context.Context,
	r *http.Request,
	callers CallerFactory,
	gvr schema.GroupVersionResource,
	req Request,
) (*unstructured.Unstructured, dynamic.Interface, error) {
	if callers == nil {
		return nil, nil, fmt.Errorf("dataplane: no caller factory configured")
	}
	bearer, headerCluster, _, err := Identity(r)
	if err != nil {
		return nil, nil, err
	}
	if headerCluster != "" && headerCluster != req.ClusterID {
		return nil, nil, fmt.Errorf("%w: path %q, header %q", ErrClusterMismatch, req.ClusterID, headerCluster)
	}
	if !IsClusterID(req.ClusterID) || req.Name == "" || req.Verb == "" {
		return nil, nil, fmt.Errorf("%w: cluster=%q name=%q verb=%q", ErrBadPath, req.ClusterID, req.Name, req.Verb)
	}

	caller, err := callers.For(req.ClusterID, bearer)
	if err != nil {
		return nil, nil, err
	}

	// Gate 1: visibility, as the caller.
	object, err := caller.Resource(gvr).Get(ctx, req.Name, metav1.GetOptions{})
	if err != nil {
		if isDenial(err) {
			return nil, nil, fmt.Errorf("%w: caller cannot get %s/%s: %w", ErrDenied, gvr.Resource, req.Name, err)
		}
		return nil, nil, fmt.Errorf("dataplane: get %s/%s as caller: %w", gvr.Resource, req.Name, err)
	}
	if object.GetDeletionTimestamp() != nil {
		return nil, nil, fmt.Errorf("%w: %s/%s is being deleted", ErrDenied, gvr.Resource, req.Name)
	}

	// Gate 2: the verb grant, as the caller.
	allowed, err := selfSubjectAllowed(ctx, caller, gvr, req.Name, req.Verb)
	if err != nil {
		if isDenial(err) {
			return nil, nil, fmt.Errorf("%w: access review refused: %w", ErrDenied, err)
		}
		return nil, nil, fmt.Errorf("dataplane: access review for %s/%s on %s: %w", gvr.Resource, req.Verb, req.Name, err)
	}
	if !allowed {
		return nil, nil, fmt.Errorf("%w: caller may not %s %s/%s", ErrDenied, SSARVerb, gvr.Resource, req.Verb)
	}
	return object, caller, nil
}

// selfSubjectAllowed asks the caller's own API server whether the caller may
// create the virtual subresource {gvr.Resource}/{verb} on name.
func selfSubjectAllowed(
	ctx context.Context,
	caller dynamic.Interface,
	gvr schema.GroupVersionResource,
	name, verb string,
) (bool, error) {
	review := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "authorization.k8s.io/v1",
		"kind":       "SelfSubjectAccessReview",
		"spec": map[string]any{
			"resourceAttributes": map[string]any{
				"group":       gvr.Group,
				"version":     gvr.Version,
				"resource":    gvr.Resource,
				"subresource": verb,
				"name":        name,
				"verb":        SSARVerb,
			},
		},
	}}
	created, err := caller.Resource(SelfSubjectAccessReviews()).Create(ctx, review, metav1.CreateOptions{})
	if err != nil {
		return false, err
	}
	allowed, _, err := unstructured.NestedBool(created.Object, "status", "allowed")
	if err != nil {
		return false, fmt.Errorf("access review has no status.allowed: %w", err)
	}
	return allowed, nil
}

// isDenial reports whether err is one of the Kubernetes outcomes that must be
// reported to the caller as a plain denial rather than a server fault.
func isDenial(err error) bool {
	return apierrors.IsForbidden(err) || apierrors.IsNotFound(err) || apierrors.IsUnauthorized(err)
}
