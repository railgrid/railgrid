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

// Package client provides typed access to the App Studio Project CRD over a
// dynamic client. It is a trimmed copy of the hub's pkg/client, carrying only
// the Project resource plus the generic TypedResource helper the provider
// needs. The provider builds these clients per request from the caller's
// bearer token (see the tenant package), so it acts as the user.
package client

import (
	"context"
	"encoding/json"
	"fmt"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/dynamic"

	aiv1alpha1 "github.com/faroshq/provider-app-studio/apis/ai/v1alpha1"
	"github.com/faroshq/provider-app-studio/tenant"
)

// ProjectGVR points at the workspace-scoped Project CRD.
var ProjectGVR = schema.GroupVersionResource{
	Group:    aiv1alpha1.GroupName,
	Version:  aiv1alpha1.Version,
	Resource: "projects",
}

// projectResource describes the Project CRD for the tenant client. The
// Project is cluster-scoped in the workspace.
var projectResource = tenant.Resource{
	GVR:        ProjectGVR,
	Kind:       "Project",
	Plural:     "Projects",
	Namespaced: false,
}

// ResourceClient is the per-resource surface App Studio needs. Its signatures
// match dynamic.ResourceInterface's subset exactly, so both a raw
// dynamic.ResourceInterface (tests built with NewFromDynamic) and the
// upsert-preserving scopeResource (production) satisfy it.
type ResourceClient interface {
	Get(ctx context.Context, name string, opts metav1.GetOptions, subresources ...string) (*unstructured.Unstructured, error)
	List(ctx context.Context, opts metav1.ListOptions) (*unstructured.UnstructuredList, error)
	Create(ctx context.Context, obj *unstructured.Unstructured, opts metav1.CreateOptions, subresources ...string) (*unstructured.Unstructured, error)
	Update(ctx context.Context, obj *unstructured.Unstructured, opts metav1.UpdateOptions, subresources ...string) (*unstructured.Unstructured, error)
	UpdateStatus(ctx context.Context, obj *unstructured.Unstructured, opts metav1.UpdateOptions) (*unstructured.Unstructured, error)
	Delete(ctx context.Context, name string, opts metav1.DeleteOptions, subresources ...string) error
	Patch(ctx context.Context, name string, pt types.PatchType, data []byte, opts metav1.PatchOptions, subresources ...string) (*unstructured.Unstructured, error)
}

// Client provides typed access to App Studio resources. It is backed by either
// a caller-scoped tenant.Scope (production: the hub's kcp proxy for the
// caller's workspace cluster) or a bare dynamic client (tests).
type Client struct {
	scope   *tenant.Scope
	dynamic dynamic.Interface
}

// NewFromScope creates a Client backed by a workspace- and caller-scoped
// tenant.Scope. Create and Update on its resources are upserts (see
// tenant.Scope.Apply).
func NewFromScope(scope *tenant.Scope) *Client {
	return &Client{scope: scope}
}

// NewFromDynamic creates a Client from an existing dynamic.Interface (tests).
// Resources returned by such a client keep the dynamic client's plain
// Create/Update semantics.
func NewFromDynamic(d dynamic.Interface) *Client {
	return &Client{dynamic: d}
}

// Resource returns a ResourceClient for an arbitrary resource (e.g. provider
// CRs, secrets). namespace is "" for cluster-scoped access.
func (c *Client) Resource(res tenant.Resource, namespace string) ResourceClient {
	if c.scope != nil {
		return &scopeResource{scope: c.scope, res: res, namespace: namespace}
	}
	nri := c.dynamic.Resource(res.GVR)
	if namespace != "" {
		return nri.Namespace(namespace)
	}
	return nri
}

// Dynamic returns the underlying dynamic client: the one given to
// NewFromDynamic, or the workspace-scoped client behind a tenant.Scope.
// Production code paths should prefer Resource() so they keep the upsert
// semantics App Studio relies on.
func (c *Client) Dynamic() dynamic.Interface {
	if c.scope != nil {
		return c.scope.Dynamic()
	}
	return c.dynamic
}

// Projects returns a typed interface for Project resources in the active
// workspace.
func (c *Client) Projects() *TypedResource[aiv1alpha1.Project, aiv1alpha1.ProjectList] {
	return &TypedResource[aiv1alpha1.Project, aiv1alpha1.ProjectList]{
		client: c.Resource(projectResource, ""),
		gvk:    ProjectGVR.GroupVersion().WithKind("Project"),
	}
}

// ListInfrastructureInstances lists the Infrastructure provider's Instances
// in the workspace with the given options.
func (c *Client) ListInfrastructureInstances(ctx context.Context, opts metav1.ListOptions) (*unstructured.UnstructuredList, error) {
	if c.scope == nil && c.dynamic == nil {
		return nil, fmt.Errorf("client is not configured")
	}
	return c.Resource(tenant.InfrastructureInstancesResource, "").List(ctx, opts)
}

// TypedResource provides typed CRUD operations for a specific resource type.
// gvk is used to populate apiVersion/kind on objects before sending them to
// the backing client.
type TypedResource[T any, L any] struct {
	client ResourceClient
	gvk    schema.GroupVersionKind
}

// Get retrieves a resource by name.
func (r *TypedResource[T, L]) Get(ctx context.Context, name string, opts metav1.GetOptions) (*T, error) {
	u, err := r.client.Get(ctx, name, opts)
	if err != nil {
		return nil, err
	}
	return fromUnstructured[T](u)
}

// List retrieves all resources matching the given options.
func (r *TypedResource[T, L]) List(ctx context.Context, opts metav1.ListOptions) (*L, error) {
	u, err := r.client.List(ctx, opts)
	if err != nil {
		return nil, err
	}
	return fromUnstructuredList[L](u)
}

// Create creates a new resource.
func (r *TypedResource[T, L]) Create(ctx context.Context, obj *T, opts metav1.CreateOptions) (*T, error) {
	u, err := r.toUnstructured(obj)
	if err != nil {
		return nil, err
	}
	result, err := r.client.Create(ctx, u, opts)
	if err != nil {
		return nil, err
	}
	return fromUnstructured[T](result)
}

// Update updates an existing resource.
func (r *TypedResource[T, L]) Update(ctx context.Context, obj *T, opts metav1.UpdateOptions) (*T, error) {
	u, err := r.toUnstructured(obj)
	if err != nil {
		return nil, err
	}
	result, err := r.client.Update(ctx, u, opts)
	if err != nil {
		return nil, err
	}
	return fromUnstructured[T](result)
}

// UpdateStatus updates the status subresource.
func (r *TypedResource[T, L]) UpdateStatus(ctx context.Context, obj *T, opts metav1.UpdateOptions) (*T, error) {
	u, err := r.toUnstructured(obj)
	if err != nil {
		return nil, err
	}
	result, err := r.client.UpdateStatus(ctx, u, opts)
	if err != nil {
		return nil, err
	}
	return fromUnstructured[T](result)
}

// Delete removes a resource by name.
func (r *TypedResource[T, L]) Delete(ctx context.Context, name string, opts metav1.DeleteOptions) error {
	return r.client.Delete(ctx, name, opts)
}

// Patch applies a patch to a resource.
func (r *TypedResource[T, L]) Patch(ctx context.Context, name string, pt types.PatchType, data []byte, opts metav1.PatchOptions, subresources ...string) (*T, error) {
	result, err := r.client.Patch(ctx, name, pt, data, opts, subresources...)
	if err != nil {
		return nil, err
	}
	return fromUnstructured[T](result)
}

func (r *TypedResource[T, L]) toUnstructured(obj *T) (*unstructured.Unstructured, error) {
	u, err := toUnstructured(obj)
	if err != nil {
		return nil, err
	}
	if u.GetAPIVersion() == "" {
		u.SetAPIVersion(r.gvk.GroupVersion().String())
	}
	if u.GetKind() == "" {
		u.SetKind(r.gvk.Kind)
	}
	return u, nil
}

func toUnstructured(obj interface{}) (*unstructured.Unstructured, error) {
	data, err := json.Marshal(obj)
	if err != nil {
		return nil, fmt.Errorf("marshaling to JSON: %w", err)
	}
	u := &unstructured.Unstructured{}
	if err := json.Unmarshal(data, &u.Object); err != nil {
		return nil, fmt.Errorf("unmarshaling to unstructured: %w", err)
	}
	return u, nil
}

func fromUnstructured[T any](u *unstructured.Unstructured) (*T, error) {
	var obj T
	if err := runtime.DefaultUnstructuredConverter.FromUnstructured(u.Object, &obj); err != nil {
		return nil, fmt.Errorf("converting from unstructured: %w", err)
	}
	return &obj, nil
}

// scopeResource adapts a tenant.Scope to the ResourceClient surface.
// Create and Update both map to Scope.Apply (create-or-update), so handlers
// that were written against an upsert keep working; status writes go through
// the status subresource.
type scopeResource struct {
	scope     *tenant.Scope
	res       tenant.Resource
	namespace string
}

func (g *scopeResource) Get(ctx context.Context, name string, _ metav1.GetOptions, _ ...string) (*unstructured.Unstructured, error) {
	return g.scope.Get(ctx, g.res, g.namespace, name)
}

func (g *scopeResource) List(ctx context.Context, opts metav1.ListOptions) (*unstructured.UnstructuredList, error) {
	items, err := g.scope.ListWithOptions(ctx, g.res, g.namespace, opts)
	if err != nil {
		return nil, err
	}
	return &unstructured.UnstructuredList{Items: items}, nil
}

func (g *scopeResource) Create(ctx context.Context, obj *unstructured.Unstructured, _ metav1.CreateOptions, subresources ...string) (*unstructured.Unstructured, error) {
	if len(subresources) == 1 && subresources[0] == "status" {
		return g.UpdateStatus(ctx, obj, metav1.UpdateOptions{})
	}
	return g.scope.Apply(ctx, g.res, g.withNamespace(obj))
}

func (g *scopeResource) Update(ctx context.Context, obj *unstructured.Unstructured, _ metav1.UpdateOptions, subresources ...string) (*unstructured.Unstructured, error) {
	if len(subresources) == 1 && subresources[0] == "status" {
		return g.UpdateStatus(ctx, obj, metav1.UpdateOptions{})
	}
	return g.scope.Apply(ctx, g.res, g.withNamespace(obj))
}

func (g *scopeResource) UpdateStatus(ctx context.Context, obj *unstructured.Unstructured, _ metav1.UpdateOptions) (*unstructured.Unstructured, error) {
	if err := g.scope.ApplyStatus(ctx, g.res, g.withNamespace(obj)); err != nil {
		return nil, err
	}
	return g.scope.Get(ctx, g.res, g.namespace, obj.GetName())
}

func (g *scopeResource) Delete(ctx context.Context, name string, opts metav1.DeleteOptions, _ ...string) error {
	return g.scope.DeleteWithOptions(ctx, g.res, g.namespace, name, opts)
}

// Patch forwards any patch type and subresource to the API server.
func (g *scopeResource) Patch(ctx context.Context, name string, pt types.PatchType, data []byte, opts metav1.PatchOptions, subresources ...string) (*unstructured.Unstructured, error) {
	nri := g.scope.Dynamic().Resource(g.res.GVR)
	if g.res.Namespaced && g.namespace != "" {
		return nri.Namespace(g.namespace).Patch(ctx, name, pt, data, opts, subresources...)
	}
	return nri.Patch(ctx, name, pt, data, opts, subresources...)
}

// withNamespace stamps the resource's namespace onto obj when the caller
// scoped the client to one and the object carries none.
func (g *scopeResource) withNamespace(obj *unstructured.Unstructured) *unstructured.Unstructured {
	if g.namespace == "" || obj.GetNamespace() != "" {
		return obj
	}
	out := obj.DeepCopy()
	out.SetNamespace(g.namespace)
	return out
}

func fromUnstructuredList[L any](u *unstructured.UnstructuredList) (*L, error) {
	content := u.UnstructuredContent()
	items := make([]interface{}, 0, len(u.Items))
	for i := range u.Items {
		items = append(items, u.Items[i].UnstructuredContent())
	}
	content["items"] = items
	data, err := json.Marshal(content)
	if err != nil {
		return nil, fmt.Errorf("marshaling list: %w", err)
	}
	var list L
	if err := json.Unmarshal(data, &list); err != nil {
		return nil, fmt.Errorf("unmarshaling list: %w", err)
	}
	return &list, nil
}
