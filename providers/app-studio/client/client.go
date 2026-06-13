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
)

// ProjectGVR points at the workspace-scoped Project CRD.
var ProjectGVR = schema.GroupVersionResource{
	Group:    aiv1alpha1.GroupName,
	Version:  aiv1alpha1.Version,
	Resource: "projects",
}

// Client provides typed access to App Studio resources via the dynamic client.
type Client struct {
	dynamic dynamic.Interface
}

// NewFromDynamic creates a Client from an existing dynamic.Interface.
func NewFromDynamic(d dynamic.Interface) *Client {
	return &Client{dynamic: d}
}

// Dynamic returns the underlying dynamic client.
func (c *Client) Dynamic() dynamic.Interface {
	return c.dynamic
}

// Projects returns a typed interface for Project resources in the active
// workspace.
func (c *Client) Projects() *TypedResource[aiv1alpha1.Project, aiv1alpha1.ProjectList] {
	return &TypedResource[aiv1alpha1.Project, aiv1alpha1.ProjectList]{
		client: c.dynamic.Resource(ProjectGVR),
		gvk:    ProjectGVR.GroupVersion().WithKind("Project"),
	}
}

// TypedResource provides typed CRUD operations for a specific resource type.
// gvk is used to populate apiVersion/kind on objects before sending them to
// the dynamic client.
type TypedResource[T any, L any] struct {
	client dynamic.ResourceInterface
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
