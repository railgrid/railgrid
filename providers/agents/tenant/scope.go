/*
Copyright 2026 The Faros Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

// Package tenant reaches a tenant's kcp workspace on the caller's behalf. The
// provider talks plain kube REST to the hub's kcp proxy at
// <hubBase>/clusters/<clusterID>, authenticating with the bearer token the
// caller presented. The proxy authorizes the caller by workspace membership
// and forwards to kcp as that user, so the provider reaches any workspace the
// caller is a member of — the same path kubectl and the portals use.
//
// Every operation exchanges unstructured objects over a dynamic client, and
// errors are the k8s apierrors the API server produced, so callers'
// IsNotFound / IsConflict / IsAlreadyExists / IsInvalid checks work directly.
package tenant

import (
	"context"
	"fmt"
	"strings"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/dynamic"

	"github.com/faroshq/provider-sdk/tenantaccess"
)

// Client is a factory for per-(cluster, caller) access to tenant workspaces
// through the hub's kcp proxy.
type Client struct {
	hubBase  string
	insecure bool
}

// NewClient targets the hub's kcp proxy under hubBase (the hub's base URL,
// e.g. https://faros-hub.faros.svc:9443). insecureSkipVerify relaxes TLS for
// in-cluster hub certs that aren't in the provider's trust store.
func NewClient(hubBase string, insecureSkipVerify bool) *Client {
	return &Client{hubBase: strings.TrimRight(hubBase, "/"), insecure: insecureSkipVerify}
}

// Resource identifies a kcp resource the Scope operates on.
type Resource struct {
	GVR        schema.GroupVersionResource
	Kind       string // e.g. "Project"
	Plural     string // e.g. "Projects"
	Namespaced bool
}

// Scope is a dynamic client bound to one workspace cluster and caller token.
type Scope struct {
	clusterID string
	dyn       dynamic.Interface
}

// For returns a client scoped to clusterID (the X-Faros-Cluster the hub
// injected) authenticating as the caller via token.
func (c *Client) For(clusterID, token string) (*Scope, error) {
	if clusterID == "" {
		return nil, fmt.Errorf("no cluster id (X-Faros-Cluster missing) — cannot target the tenant workspace")
	}
	if token == "" {
		return nil, fmt.Errorf("no bearer token on request — cannot act on the tenant's behalf")
	}
	dyn, err := tenantaccess.NewDynamicClient(c.hubBase, clusterID, token, c.insecure)
	if err != nil {
		return nil, fmt.Errorf("building tenant client for cluster %q: %w", clusterID, err)
	}
	return &Scope{clusterID: clusterID, dyn: dyn}, nil
}

// NewScopeFromDynamic wraps an existing dynamic client as a Scope. Tests use
// it to substitute a fake; production code goes through Client.For.
func NewScopeFromDynamic(clusterID string, d dynamic.Interface) *Scope {
	return &Scope{clusterID: clusterID, dyn: d}
}

// ClusterID is the workspace cluster this scope targets.
func (s *Scope) ClusterID() string { return s.clusterID }

// Dynamic exposes the underlying dynamic client for callers that need
// operations the Scope does not wrap.
func (s *Scope) Dynamic() dynamic.Interface { return s.dyn }

func (s *Scope) resource(res Resource, namespace string) dynamic.ResourceInterface {
	if res.Namespaced {
		return s.dyn.Resource(res.GVR).Namespace(namespace)
	}
	return s.dyn.Resource(res.GVR)
}

// Get fetches one object.
func (s *Scope) Get(ctx context.Context, res Resource, namespace, name string) (*unstructured.Unstructured, error) {
	return s.resource(res, namespace).Get(ctx, name, metav1.GetOptions{})
}

// List fetches all objects of res. namespace is optional (empty lists across
// all namespaces for namespaced resources).
func (s *Scope) List(ctx context.Context, res Resource, namespace string) ([]unstructured.Unstructured, error) {
	return s.ListWithOptions(ctx, res, namespace, metav1.ListOptions{})
}

// ListWithOptions is List with server-side ListOptions (label/field
// selectors, limits).
func (s *Scope) ListWithOptions(ctx context.Context, res Resource, namespace string, opts metav1.ListOptions) ([]unstructured.Unstructured, error) {
	list, err := s.resource(res, namespace).List(ctx, opts)
	if err != nil {
		return nil, err
	}
	return list.Items, nil
}

// Apply create-or-updates obj: an absent object is created, an existing one
// is replaced wholesale (the server's resourceVersion and uid are carried
// over, so this is last-write-wins, not compare-and-swap). An object without
// a name is created as-is (generateName).
func (s *Scope) Apply(ctx context.Context, obj *unstructured.Unstructured) (*unstructured.Unstructured, error) {
	res := resourceFromObject(obj)
	ri := s.resource(res, obj.GetNamespace())
	if obj.GetName() == "" {
		return ri.Create(ctx, obj, metav1.CreateOptions{})
	}

	current, err := ri.Get(ctx, obj.GetName(), metav1.GetOptions{})
	switch {
	case apierrors.IsNotFound(err):
		desired := obj.DeepCopy()
		desired.SetResourceVersion("")
		created, err := ri.Create(ctx, desired, metav1.CreateOptions{})
		if !apierrors.IsAlreadyExists(err) {
			return created, err
		}
		// Lost a create race; fall through to the update path.
		if current, err = ri.Get(ctx, obj.GetName(), metav1.GetOptions{}); err != nil {
			return nil, err
		}
	case err != nil:
		return nil, err
	}

	desired := obj.DeepCopy()
	desired.SetResourceVersion(current.GetResourceVersion())
	desired.SetUID(current.GetUID())
	return ri.Update(ctx, desired, metav1.UpdateOptions{})
}

// ApplyStatus merge-patches obj's status subresource with obj's status.
func (s *Scope) ApplyStatus(ctx context.Context, obj *unstructured.Unstructured) error {
	status, found, err := unstructured.NestedFieldNoCopy(obj.Object, "status")
	if err != nil {
		return fmt.Errorf("reading status of %s %q: %w", obj.GetKind(), obj.GetName(), err)
	}
	if !found {
		status = map[string]any{}
	}
	patch := &unstructured.Unstructured{Object: map[string]any{"status": status}}
	data, err := patch.MarshalJSON()
	if err != nil {
		return err
	}
	res := resourceFromObject(obj)
	_, err = s.resource(res, obj.GetNamespace()).Patch(ctx, obj.GetName(), types.MergePatchType, data, metav1.PatchOptions{}, "status")
	return err
}

// Delete removes one object.
func (s *Scope) Delete(ctx context.Context, res Resource, namespace, name string) error {
	return s.resource(res, namespace).Delete(ctx, name, metav1.DeleteOptions{})
}

// resourceFromObject derives a Resource from an object's GVK. The resource
// name is the lower-cased plural of the Kind, which holds for every type this
// provider writes; the object is namespaced iff it carries a namespace.
func resourceFromObject(obj *unstructured.Unstructured) Resource {
	gvk := obj.GroupVersionKind()
	return Resource{
		GVR:        schema.GroupVersionResource{Group: gvk.Group, Version: gvk.Version, Resource: strings.ToLower(gvk.Kind) + "s"},
		Kind:       gvk.Kind,
		Plural:     gvk.Kind + "s",
		Namespaced: obj.GetNamespace() != "",
	}
}
