// Copyright 2026 The Railgrid Authors.
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy at http://www.apache.org/licenses/LICENSE-2.0

package dataplane

import (
	"context"
	"fmt"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
)

// SelfSubjectAccessReviews is the review a caller-credentialed client (an MCP
// tool acting with the bearer the hub aggregate forwarded) asks about itself.
// It is exported because a test double for a CallerFactory has to answer
// exactly this resource.
func SelfSubjectAccessReviews() schema.GroupVersionResource {
	return schema.GroupVersionResource{
		Group: "authorization.k8s.io", Version: "v1", Resource: "selfsubjectaccessreviews",
	}
}

// SSARVerb is the RBAC verb a POST to a verb maps onto. kcp authorizes a verb
// call by mapping the HTTP method onto the RBAC verb (GET → get, POST →
// create, PUT → update, PATCH → patch, DELETE → delete, ?watch= → watch, an
// upgrade → its method) on the subresource {resource}/{verb}. The coordinate
// is the capability and the method is the provider's transport detail, so a
// grant on a verb coordinate carries SubresourceVerbs, never one method.
const SSARVerb = "create"

// SubresourceVerbs is the RBAC verb set a grant on a verb coordinate
// "{resource}/{verb}" carries: every verb kcp can map an HTTP method onto.
// The hub mints exactly this list wherever it grants a data-plane verb
// (pkg/hub/serviceaccounts/workload_identity.go, pkg/hub/identity/policy.go,
// pkg/hub/controllers/mcpserver/rbac.go); it is spelled out rather than "*"
// so a generated role never carries a wildcard. A composition claim on a verb
// coordinate (spec.dependencies[].composes[]) is spelled verbs ["*"], which
// kcp's claim authorizer accepts as the same thing.
var SubresourceVerbs = []string{"get", "list", "watch", "create", "update", "patch", "delete"}

// Gate runs the contract's gate for req and returns the addressed object
// together with a client acting as the provider in req.ClusterID.
//
// A verb is only ever reached as a kcp custom subresource: the shard
// authenticated the caller, authorized SSARVerb on {resource}/{verb} with
// ordinary RBAC, and forwarded the request with the caller's identity stamped
// into requestheader headers. serve's subresource adapter put that identity
// into ctx (WithProxiedIdentity); a request that carries none is refused with
// ErrNoCaller — anonymous is not a fallback, and the provider's own identity
// is never a substitute for the caller's.
//
// Gate 1 — visibility — keeps its meaning and changes its mechanism. The
// provider holds the caller's name and groups and no credential to read with,
// so it creates a SubjectAccessReview for "get" on gvr/req.Name on the
// caller's behalf and then reads the object as itself, both through its
// APIExport virtual workspace. An object with a deletionTimestamp is denied:
// a verb must not run against something already on its way out.
//
// Gate 2 — the verb grant — is not repeated: kcp authorized the noun before it
// proxied the request at all.
//
// A foreign provider (a ServiceAccount from another logical cluster) reached
// this verb through ITS OWN export virtual workspace, which kcp built only
// because the tenant accepted that provider's claim on "{resource}/{verb}".
// The claim is the authorization; gate 1 reduces to reading the parent as
// ourselves.
//
// Every failure a caller could use to probe — no such object, no visibility —
// comes back as ErrDenied, which WriteError answers with 404. Returned errors
// wrap the sentinels with detail meant for the provider's log, never for the
// response body.
func Gate(
	ctx context.Context,
	callers ProviderCallerFactory,
	gvr schema.GroupVersionResource,
	req Request,
) (*unstructured.Unstructured, dynamic.Interface, error) {
	if callers == nil {
		return nil, nil, fmt.Errorf("dataplane: no provider caller factory configured")
	}
	if !IsClusterID(req.ClusterID) || req.Name == "" || req.Verb == "" {
		return nil, nil, fmt.Errorf("%w: cluster=%q name=%q verb=%q", ErrBadPath, req.ClusterID, req.Name, req.Verb)
	}
	identity, ok := ProxiedIdentityFrom(ctx)
	if !ok || identity.User == "" {
		return nil, nil, ErrNoCaller
	}
	provider, err := callers.AsProvider(req.ClusterID)
	if err != nil {
		return nil, nil, err
	}
	if identity.IsForeignProvider(req.ClusterID) {
		return readParentAsProvider(ctx, provider, gvr, req)
	}
	allowed, err := Authorize(ctx, provider, identity, ResourceAttributes{
		Group: gvr.Group, Version: gvr.Version, Resource: gvr.Resource, Name: req.Name, Verb: "get",
	})
	if err != nil {
		return nil, nil, fmt.Errorf("dataplane: access review for %s on %s/%s: %w", identity.User, gvr.Resource, req.Name, err)
	}
	if !allowed {
		return nil, nil, fmt.Errorf("%w: %s cannot get %s/%s", ErrDenied, identity.User, gvr.Resource, req.Name)
	}
	return readParentAsProvider(ctx, provider, gvr, req)
}

// ResourceAttributes are the resourceAttributes of a SubjectAccessReview: what
// Authorize asks kcp about on the caller's behalf.
type ResourceAttributes struct {
	Group       string
	Version     string
	Resource    string
	Subresource string
	Namespace   string
	Name        string
	Verb        string
}

// Authorize asks kcp, through the provider's client, whether identity may
// perform attrs. It is how a handler decides anything about the caller beyond
// what Gate already settled — a second object the verb touches, a write the
// verb performs on the caller's behalf — because there is no caller credential
// to try it with.
//
// The client must act as the provider through its export virtual workspace
// (ProviderCallerFactory.AsProvider): kcp serves SubjectAccessReview there
// only for an export that claims authorization.k8s.io/subjectaccessreviews
// (create) and a binding that accepted it. Without the claim the review is an
// error, never a silent allow.
func Authorize(ctx context.Context, provider dynamic.Interface, identity ProxiedIdentity, attrs ResourceAttributes) (bool, error) {
	if provider == nil {
		return false, fmt.Errorf("dataplane: no provider client to review with")
	}
	if identity.User == "" {
		return false, ErrNoCaller
	}
	resource := map[string]any{
		"group":    attrs.Group,
		"version":  attrs.Version,
		"resource": attrs.Resource,
		"name":     attrs.Name,
		"verb":     attrs.Verb,
	}
	if attrs.Subresource != "" {
		resource["subresource"] = attrs.Subresource
	}
	if attrs.Namespace != "" {
		resource["namespace"] = attrs.Namespace
	}
	spec := map[string]any{
		"user":               identity.User,
		"resourceAttributes": resource,
	}
	if len(identity.Groups) > 0 {
		groups := make([]any, 0, len(identity.Groups))
		for _, g := range identity.Groups {
			groups = append(groups, g)
		}
		spec["groups"] = groups
	}
	if len(identity.Extra) > 0 {
		extra := make(map[string]any, len(identity.Extra))
		for k, values := range identity.Extra {
			vs := make([]any, 0, len(values))
			for _, v := range values {
				vs = append(vs, v)
			}
			extra[k] = vs
		}
		spec["extra"] = extra
	}
	review := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "authorization.k8s.io/v1",
		"kind":       "SubjectAccessReview",
		"spec":       spec,
	}}
	created, err := provider.Resource(SubjectAccessReviews()).Create(ctx, review, metav1.CreateOptions{})
	if err != nil {
		return false, err
	}
	allowed, _, err := unstructured.NestedBool(created.Object, "status", "allowed")
	if err != nil {
		return false, fmt.Errorf("dataplane: access review has no status.allowed: %w", err)
	}
	return allowed, nil
}

// SubjectAccessReviews is what Authorize creates: the review a provider runs
// on a caller's behalf, as opposed to the Self variant a caller-credentialed
// client runs about itself.
func SubjectAccessReviews() schema.GroupVersionResource {
	return schema.GroupVersionResource{
		Group: "authorization.k8s.io", Version: "v1", Resource: "subjectaccessreviews",
	}
}

// readParentAsProvider is gate 1's read, as the provider, once visibility is
// settled.
func readParentAsProvider(
	ctx context.Context,
	provider dynamic.Interface,
	gvr schema.GroupVersionResource,
	req Request,
) (*unstructured.Unstructured, dynamic.Interface, error) {
	object, err := provider.Resource(gvr).Get(ctx, req.Name, metav1.GetOptions{})
	if err != nil {
		if isDenial(err) {
			return nil, nil, fmt.Errorf("%w: %s/%s: %w", ErrDenied, gvr.Resource, req.Name, err)
		}
		return nil, nil, fmt.Errorf("dataplane: get %s/%s as provider: %w", gvr.Resource, req.Name, err)
	}
	if object.GetDeletionTimestamp() != nil {
		return nil, nil, fmt.Errorf("%w: %s/%s is being deleted", ErrDenied, gvr.Resource, req.Name)
	}
	return object, provider, nil
}

// isDenial reports whether err is one of the statuses a caller could use to
// probe for existence or standing. All of them collapse to ErrDenied.
func isDenial(err error) bool {
	return apierrors.IsNotFound(err) || apierrors.IsForbidden(err) || apierrors.IsUnauthorized(err)
}
