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

// Package client provides dynamic Kubernetes client utilities.
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
	"k8s.io/client-go/rest"

	kedgev1alpha1 "github.com/faroshq/faros-kedge/apis/kedge/v1alpha1"
	tenancyv1alpha1 "github.com/faroshq/faros-kedge/apis/tenancy/v1alpha1"
)

var (
	VirtualWorkloadGVR = schema.GroupVersionResource{
		Group:    kedgev1alpha1.GroupName,
		Version:  kedgev1alpha1.Version,
		Resource: "virtualworkloads",
	}
	SiteGVR = schema.GroupVersionResource{
		Group:    kedgev1alpha1.GroupName,
		Version:  kedgev1alpha1.Version,
		Resource: "sites",
	}
	PlacementGVR = schema.GroupVersionResource{
		Group:    kedgev1alpha1.GroupName,
		Version:  kedgev1alpha1.Version,
		Resource: "placements",
	}
	UserGVR = schema.GroupVersionResource{
		Group:    "kedge.faros.sh",
		Version:  "v1alpha1",
		Resource: "users",
	}
)

// Client provides typed access to kedge custom resources via the dynamic client.
type Client struct {
	dynamic dynamic.Interface
}

// NewForConfig creates a new Client for the given rest config.
func NewForConfig(config *rest.Config) (*Client, error) {
	d, err := dynamic.NewForConfig(config)
	if err != nil {
		return nil, fmt.Errorf("creating dynamic client: %w", err)
	}
	return &Client{dynamic: d}, nil
}

// NewFromDynamic creates a new Client from an existing dynamic.Interface.
func NewFromDynamic(d dynamic.Interface) *Client {
	return &Client{dynamic: d}
}

// Dynamic returns the underlying dynamic client.
func (c *Client) Dynamic() dynamic.Interface {
	return c.dynamic
}

// VirtualWorkloads returns a typed interface for VirtualWorkload resources in a namespace.
func (c *Client) VirtualWorkloads(namespace string) *TypedResource[kedgev1alpha1.VirtualWorkload, kedgev1alpha1.VirtualWorkloadList] {
	return &TypedResource[kedgev1alpha1.VirtualWorkload, kedgev1alpha1.VirtualWorkloadList]{
		client: c.dynamic.Resource(VirtualWorkloadGVR).Namespace(namespace),
	}
}

// Sites returns a typed interface for Site resources (cluster-scoped).
func (c *Client) Sites() *TypedResource[kedgev1alpha1.Site, kedgev1alpha1.SiteList] {
	return &TypedResource[kedgev1alpha1.Site, kedgev1alpha1.SiteList]{
		client: c.dynamic.Resource(SiteGVR),
	}
}

// Placements returns a typed interface for Placement resources in a namespace.
func (c *Client) Placements(namespace string) *TypedResource[kedgev1alpha1.Placement, kedgev1alpha1.PlacementList] {
	return &TypedResource[kedgev1alpha1.Placement, kedgev1alpha1.PlacementList]{
		client: c.dynamic.Resource(PlacementGVR).Namespace(namespace),
	}
}

// Users returns a typed interface for User resources (cluster-scoped).
func (c *Client) Users() *TypedResource[tenancyv1alpha1.User, tenancyv1alpha1.UserList] {
	return &TypedResource[tenancyv1alpha1.User, tenancyv1alpha1.UserList]{
		client: c.dynamic.Resource(UserGVR),
	}
}

// TypedResource provides typed CRUD operations for a specific resource type.
type TypedResource[T any, L any] struct {
	client dynamic.ResourceInterface
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
	u, err := toUnstructured(obj)
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
	u, err := toUnstructured(obj)
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
	u, err := toUnstructured(obj)
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
	data, err := json.Marshal(u)
	if err != nil {
		return nil, fmt.Errorf("marshaling list: %w", err)
	}
	var list L
	if err := json.Unmarshal(data, &list); err != nil {
		return nil, fmt.Errorf("unmarshaling list: %w", err)
	}
	return &list, nil
}
