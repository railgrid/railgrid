/*
Copyright 2026 The Faros Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

// Package tenant reaches a tenant's kcp workspace through the hub's
// caller-scoped kcp proxy at <hubBase>/clusters/<clusterID>, authenticating as
// the caller. The proxy authorizes the request by workspace membership and
// forwards it to kcp as that user, so App Studio acts with exactly the
// caller's RBAC in any workspace the caller can reach.
//
// Every operation is plain Kubernetes REST through a dynamic client, so
// errors arrive as apierrors.StatusError and callers' IsNotFound /
// IsConflict / IsAlreadyExists checks work unchanged.
package tenant

import (
	"context"
	"encoding/json"
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

// NewClient targets the hub under hubBase (the hub's base URL, e.g.
// https://faros-hub.faros.svc:9443). insecureSkipVerify relaxes TLS for
// in-cluster hub certs that aren't in the provider's trust store.
func NewClient(hubBase string, insecureSkipVerify bool) *Client {
	return &Client{hubBase: strings.TrimRight(hubBase, "/"), insecure: insecureSkipVerify}
}

// Resource identifies a kcp resource App Studio reads or writes. GVR drives
// the REST path; Kind and Plural are kept for callers that describe resources
// by kind (error messages, object construction) and must match the CRD.
type Resource struct {
	GVR        schema.GroupVersionResource
	Kind       string // e.g. "Project"
	Plural     string // e.g. "Projects"
	Namespaced bool
}

// Scope is a client bound to one workspace cluster and caller token.
type Scope struct {
	dyn dynamic.Interface
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
		return nil, fmt.Errorf("tenant client for cluster %q: %w", clusterID, err)
	}
	return &Scope{dyn: dyn}, nil
}

// NewScopeFromDynamic wraps an existing dynamic client (tests, or callers that
// already hold a workspace-scoped client).
func NewScopeFromDynamic(d dynamic.Interface) *Scope {
	return &Scope{dyn: d}
}

// Dynamic returns the workspace-scoped dynamic client backing the Scope.
func (s *Scope) Dynamic() dynamic.Interface {
	return s.dyn
}

// resource resolves the dynamic ResourceInterface for res, namespaced when
// both the resource is namespaced and a namespace is given (an empty
// namespace on a namespaced resource means all namespaces for List).
func (s *Scope) resource(res Resource, namespace string) dynamic.ResourceInterface {
	nri := s.dyn.Resource(res.GVR)
	if res.Namespaced && namespace != "" {
		return nri.Namespace(namespace)
	}
	return nri
}

// Get fetches one object.
func (s *Scope) Get(ctx context.Context, res Resource, namespace, name string) (*unstructured.Unstructured, error) {
	return s.resource(res, namespace).Get(ctx, name, metav1.GetOptions{})
}

// List fetches all objects. namespace is optional (empty lists across all
// namespaces for namespaced resources). Callers that need list filters should
// use ListWithOptions.
func (s *Scope) List(ctx context.Context, res Resource, namespace string) ([]unstructured.Unstructured, error) {
	return s.ListWithOptions(ctx, res, namespace, metav1.ListOptions{})
}

// ListWithOptions fetches objects with the given list options; label and
// field selectors are applied server-side.
func (s *Scope) ListWithOptions(ctx context.Context, res Resource, namespace string, listOptions metav1.ListOptions) ([]unstructured.Unstructured, error) {
	list, err := s.resource(res, namespace).List(ctx, listOptions)
	if err != nil {
		return nil, err
	}
	return list.Items, nil
}

// InfrastructureInstancesResource describes the Infrastructure provider's
// cluster-scoped Instance CRD.
var InfrastructureInstancesResource = Resource{
	GVR:    schema.GroupVersionResource{Group: "infrastructure.faros.sh", Version: "v1alpha1", Resource: "instances"},
	Kind:   "Instance",
	Plural: "Instances",
}

// ListInfrastructureInstances lists Infrastructure Instances with the given
// options (callers read metadata and status.phase, which the full objects
// carry).
func (s *Scope) ListInfrastructureInstances(ctx context.Context, listOptions metav1.ListOptions) ([]unstructured.Unstructured, error) {
	return s.ListWithOptions(ctx, InfrastructureInstancesResource, "", listOptions)
}

// Apply create-or-updates obj. It is an upsert, not a compare-and-swap: when
// obj carries no resourceVersion and already exists, the current object's
// resourceVersion is copied onto obj before the update. An obj that already
// carries a resourceVersion is sent as a plain update, so callers that want
// optimistic concurrency get a Conflict on a stale version.
func (s *Scope) Apply(ctx context.Context, res Resource, obj *unstructured.Unstructured) (*unstructured.Unstructured, error) {
	if obj == nil {
		return nil, fmt.Errorf("apply: object is nil")
	}
	if obj.GetName() == "" {
		return nil, fmt.Errorf("apply %s: object has no name", res.GVR.Resource)
	}
	if res.Namespaced && obj.GetNamespace() == "" {
		return nil, fmt.Errorf("apply %s %q: namespace is required", res.GVR.Resource, obj.GetName())
	}
	ri := s.resource(res, obj.GetNamespace())
	if obj.GetResourceVersion() != "" {
		return ri.Update(ctx, obj, metav1.UpdateOptions{})
	}
	created, err := ri.Create(ctx, obj, metav1.CreateOptions{})
	if err == nil {
		return created, nil
	}
	if !apierrors.IsAlreadyExists(err) {
		return nil, err
	}
	current, err := ri.Get(ctx, obj.GetName(), metav1.GetOptions{})
	if err != nil {
		return nil, err
	}
	desired := obj.DeepCopy()
	desired.SetResourceVersion(current.GetResourceVersion())
	if desired.GetUID() == "" {
		desired.SetUID(current.GetUID())
	}
	return ri.Update(ctx, desired, metav1.UpdateOptions{})
}

// ApplyStatus merge-patches obj's status subresource with obj.status.
func (s *Scope) ApplyStatus(ctx context.Context, res Resource, obj *unstructured.Unstructured) error {
	if obj == nil {
		return fmt.Errorf("apply status: object is nil")
	}
	if obj.GetName() == "" {
		return fmt.Errorf("apply status %s: object has no name", res.GVR.Resource)
	}
	status, found, err := unstructured.NestedFieldNoCopy(obj.Object, "status")
	if err != nil {
		return fmt.Errorf("apply status %s %q: %w", res.GVR.Resource, obj.GetName(), err)
	}
	if !found {
		status = map[string]any{}
	}
	patch, err := json.Marshal(map[string]any{"status": status})
	if err != nil {
		return fmt.Errorf("encode status patch: %w", err)
	}
	_, err = s.resource(res, obj.GetNamespace()).Patch(ctx, obj.GetName(), types.MergePatchType, patch, metav1.PatchOptions{}, "status")
	return err
}

// Delete removes one object.
func (s *Scope) Delete(ctx context.Context, res Resource, namespace, name string) error {
	return s.DeleteWithOptions(ctx, res, namespace, name, metav1.DeleteOptions{})
}

// DeleteWithOptions removes one object, carrying DeleteOptions (UID and
// resourceVersion preconditions, propagation policy) to the API server.
func (s *Scope) DeleteWithOptions(ctx context.Context, res Resource, namespace, name string, opts metav1.DeleteOptions) error {
	if res.Namespaced && namespace == "" {
		return fmt.Errorf("namespace is required to delete %s %q", res.Kind, name)
	}
	return s.resource(res, namespace).Delete(ctx, name, opts)
}
