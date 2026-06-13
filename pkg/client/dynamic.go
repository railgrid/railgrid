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

	aiv1alpha1 "github.com/faroshq/faros-kedge/apis/ai/v1alpha1"
	kedgev1alpha1 "github.com/faroshq/faros-kedge/apis/kedge/v1alpha1"
	tenancyv1alpha1 "github.com/faroshq/faros-kedge/apis/tenancy/v1alpha1"
)

var (
	VirtualWorkloadGVR = schema.GroupVersionResource{
		Group:    kedgev1alpha1.GroupName,
		Version:  kedgev1alpha1.Version,
		Resource: "virtualworkloads",
	}
	EdgeGVR = schema.GroupVersionResource{
		Group:    kedgev1alpha1.GroupName,
		Version:  kedgev1alpha1.Version,
		Resource: "edges",
	}

	PlacementGVR = schema.GroupVersionResource{
		Group:    kedgev1alpha1.GroupName,
		Version:  kedgev1alpha1.Version,
		Resource: "placements",
	}
	// UserGVR points at the new tenancy.kedge.faros.sh User CRD. PRs
	// #204-#207 introduced the tenancy.kedge.faros.sh group; this GVR
	// previously pointed at the legacy kedge.faros.sh group, which left
	// User writes from the auth handler invisible to the org bootstrap
	// controller (which watches the new group). Migration in roadmap
	// step 7+ aligns both sides on tenancy.kedge.faros.sh.
	UserGVR = schema.GroupVersionResource{
		Group:    "tenancy.kedge.faros.sh",
		Version:  "v1alpha1",
		Resource: "users",
	}

	// UserMembershipIndexGVR points at the cluster-scoped UMI CRD
	// (see apis/tenancy/v1alpha1/types_user_membership_index.go).
	// One UMI per User; the tenant middleware reads this on every
	// request to authorise (Org, Workspace) header pairs.
	UserMembershipIndexGVR = schema.GroupVersionResource{
		Group:    "tenancy.kedge.faros.sh",
		Version:  "v1alpha1",
		Resource: "usermembershipindices",
	}

	// OrganizationGVR points at the cluster-scoped Organization CRD
	// (see apis/tenancy/v1alpha1/types_organization.go). Used by the
	// step 10 REST surface for Org CRUD against root:kedge:users.
	OrganizationGVR = schema.GroupVersionResource{
		Group:    "tenancy.kedge.faros.sh",
		Version:  "v1alpha1",
		Resource: "organizations",
	}

	// ProjectGVR points at the workspace-scoped Project CRD.
	ProjectGVR = schema.GroupVersionResource{
		Group:    aiv1alpha1.GroupName,
		Version:  aiv1alpha1.Version,
		Resource: "projects",
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
		gvk:    VirtualWorkloadGVR.GroupVersion().WithKind("VirtualWorkload"),
	}
}

// Edges returns a typed interface for Edge resources (cluster-scoped).
func (c *Client) Edges() *TypedResource[kedgev1alpha1.Edge, kedgev1alpha1.EdgeList] {
	return &TypedResource[kedgev1alpha1.Edge, kedgev1alpha1.EdgeList]{
		client: c.dynamic.Resource(EdgeGVR),
		gvk:    EdgeGVR.GroupVersion().WithKind("Edge"),
	}
}

// Placements returns a typed interface for Placement resources in a namespace.
func (c *Client) Placements(namespace string) *TypedResource[kedgev1alpha1.Placement, kedgev1alpha1.PlacementList] {
	return &TypedResource[kedgev1alpha1.Placement, kedgev1alpha1.PlacementList]{
		client: c.dynamic.Resource(PlacementGVR).Namespace(namespace),
		gvk:    PlacementGVR.GroupVersion().WithKind("Placement"),
	}
}

// Users returns a typed interface for User resources (cluster-scoped).
func (c *Client) Users() *TypedResource[tenancyv1alpha1.User, tenancyv1alpha1.UserList] {
	return &TypedResource[tenancyv1alpha1.User, tenancyv1alpha1.UserList]{
		client: c.dynamic.Resource(UserGVR),
		gvk:    UserGVR.GroupVersion().WithKind("User"),
	}
}

// UserMembershipIndices returns a typed interface for the UMI CRD
// (cluster-scoped). One UMI per User; the tenant middleware uses
// this to authorise X-Kedge-Org / X-Kedge-Workspace headers on every
// /api/* request.
func (c *Client) UserMembershipIndices() *TypedResource[tenancyv1alpha1.UserMembershipIndex, tenancyv1alpha1.UserMembershipIndexList] {
	return &TypedResource[tenancyv1alpha1.UserMembershipIndex, tenancyv1alpha1.UserMembershipIndexList]{
		client: c.dynamic.Resource(UserMembershipIndexGVR),
		gvk:    UserMembershipIndexGVR.GroupVersion().WithKind("UserMembershipIndex"),
	}
}

// Organizations returns a typed interface for the cluster-scoped
// Organization CRD. Used by the step 10 REST surface.
func (c *Client) Organizations() *TypedResource[tenancyv1alpha1.Organization, tenancyv1alpha1.OrganizationList] {
	return &TypedResource[tenancyv1alpha1.Organization, tenancyv1alpha1.OrganizationList]{
		client: c.dynamic.Resource(OrganizationGVR),
		gvk:    OrganizationGVR.GroupVersion().WithKind("Organization"),
	}
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
// the dynamic client. The Go structs have TypeMeta tagged `omitempty`, so
// callers that forget to set apiVersion/kind would otherwise produce a JSON
// payload missing both fields — which the API server rejects with
// "Object 'Kind' is missing".
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

// toUnstructured converts a typed object to unstructured, defaulting
// apiVersion + kind from the resource's GVK so callers don't have to
// remember to set them. (The Go structs' TypeMeta fields are tagged
// `omitempty`; without this defaulting, a forgotten TypeMeta produces
// a payload missing apiVersion/kind, which the API server rejects.)
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
