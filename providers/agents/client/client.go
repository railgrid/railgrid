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

// Package client provides typed, workspace-scoped access to the agents
// provider's CRDs and to tenant Secrets, over the hub's GraphQL gateway. The
// provider builds a Client per request from the caller's bearer token (see the
// tenant package), so it always acts as the calling user.
package client

import (
	"context"
	"encoding/json"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"

	agentsv1alpha1 "github.com/faroshq/provider-agents/apis/v1alpha1"
	"github.com/faroshq/provider-agents/tenant"
)

// GVRs for the agents provider resources.
var (
	AgentGVR      = agentsGVR("agents")
	ConnectionGVR = agentsGVR("connections")
	ScheduleGVR   = agentsGVR("schedules")
	TriggerGVR    = agentsGVR("triggers")
	RunGVR        = agentsGVR("runs")
	ToolsetGVR    = agentsGVR("toolsets")
	SecretGVR     = schema.GroupVersionResource{Group: "", Version: "v1", Resource: "secrets"}
)

func agentsGVR(resource string) schema.GroupVersionResource {
	return schema.GroupVersionResource{Group: agentsv1alpha1.GroupName, Version: agentsv1alpha1.Version, Resource: resource}
}

var (
	agentResource    = tenant.Resource{GVR: AgentGVR, Kind: "Agent", Plural: "Agents", Namespaced: false}
	connectionRes    = tenant.Resource{GVR: ConnectionGVR, Kind: "Connection", Plural: "Connections", Namespaced: false}
	scheduleResource = tenant.Resource{GVR: ScheduleGVR, Kind: "Schedule", Plural: "Schedules", Namespaced: false}
	triggerResource  = tenant.Resource{GVR: TriggerGVR, Kind: "Trigger", Plural: "Triggers", Namespaced: false}
	runResource      = tenant.Resource{GVR: RunGVR, Kind: "Run", Plural: "Runs", Namespaced: false}
	toolsetResource  = tenant.Resource{GVR: ToolsetGVR, Kind: "Toolset", Plural: "Toolsets", Namespaced: false}
	secretResource   = tenant.Resource{GVR: SecretGVR, Kind: "Secret", Plural: "Secrets", Namespaced: true}
)

// Client provides typed access to the agents provider's tenant-workspace
// resources over the GraphQL gateway.
type Client struct {
	scope *tenant.Scope
}

// NewFromGraphQL builds a Client from a resolved tenant Scope.
func NewFromGraphQL(scope *tenant.Scope) *Client {
	return &Client{scope: scope}
}

// Agents returns a typed interface for Agent resources.
func (c *Client) Agents() *TypedResource[agentsv1alpha1.Agent, agentsv1alpha1.AgentList] {
	return &TypedResource[agentsv1alpha1.Agent, agentsv1alpha1.AgentList]{
		scope: c.scope, res: agentResource,
		gvk: AgentGVR.GroupVersion().WithKind("Agent"),
	}
}

// Connections returns a typed interface for Connection resources.
func (c *Client) Connections() *TypedResource[agentsv1alpha1.Connection, agentsv1alpha1.ConnectionList] {
	return &TypedResource[agentsv1alpha1.Connection, agentsv1alpha1.ConnectionList]{
		scope: c.scope, res: connectionRes,
		gvk: ConnectionGVR.GroupVersion().WithKind("Connection"),
	}
}

// Schedules returns a typed interface for Schedule resources.
func (c *Client) Schedules() *TypedResource[agentsv1alpha1.Schedule, agentsv1alpha1.ScheduleList] {
	return &TypedResource[agentsv1alpha1.Schedule, agentsv1alpha1.ScheduleList]{
		scope: c.scope, res: scheduleResource,
		gvk: ScheduleGVR.GroupVersion().WithKind("Schedule"),
	}
}

// Triggers returns a typed interface for Trigger resources.
func (c *Client) Triggers() *TypedResource[agentsv1alpha1.Trigger, agentsv1alpha1.TriggerList] {
	return &TypedResource[agentsv1alpha1.Trigger, agentsv1alpha1.TriggerList]{
		scope: c.scope, res: triggerResource,
		gvk: TriggerGVR.GroupVersion().WithKind("Trigger"),
	}
}

// Runs returns a typed interface for Run resources.
func (c *Client) Runs() *TypedResource[agentsv1alpha1.Run, agentsv1alpha1.RunList] {
	return &TypedResource[agentsv1alpha1.Run, agentsv1alpha1.RunList]{
		scope: c.scope, res: runResource,
		gvk: RunGVR.GroupVersion().WithKind("Run"),
	}
}

// Toolsets returns a typed interface for Toolset resources.
func (c *Client) Toolsets() *TypedResource[agentsv1alpha1.Toolset, agentsv1alpha1.ToolsetList] {
	return &TypedResource[agentsv1alpha1.Toolset, agentsv1alpha1.ToolsetList]{
		scope: c.scope, res: toolsetResource,
		gvk: ToolsetGVR.GroupVersion().WithKind("Toolset"),
	}
}

// GetSecret fetches a Secret from the tenant workspace namespace.
func (c *Client) GetSecret(ctx context.Context, namespace, name string) (*corev1.Secret, error) {
	u, err := c.scope.Get(ctx, secretResource, namespace, name)
	if err != nil {
		return nil, err
	}
	return fromUnstructured[corev1.Secret](u)
}

// DeleteSecret removes a Secret from the tenant workspace namespace.
func (c *Client) DeleteSecret(ctx context.Context, namespace, name string) error {
	return c.scope.Delete(ctx, secretResource, namespace, name)
}

// ListSecrets lists Secrets in the tenant workspace namespace.
func (c *Client) ListSecrets(ctx context.Context, namespace string) ([]corev1.Secret, error) {
	items, err := c.scope.List(ctx, secretResource, namespace)
	if err != nil {
		return nil, err
	}
	out := make([]corev1.Secret, 0, len(items))
	for i := range items {
		s, err := fromUnstructured[corev1.Secret](&items[i])
		if err != nil {
			return nil, err
		}
		out = append(out, *s)
	}
	return out, nil
}

// ApplySecret create-or-updates a Secret in the tenant workspace namespace.
func (c *Client) ApplySecret(ctx context.Context, s *corev1.Secret) (*corev1.Secret, error) {
	if s.APIVersion == "" {
		s.APIVersion = "v1"
	}
	if s.Kind == "" {
		s.Kind = "Secret"
	}
	u, err := toUnstructured(s)
	if err != nil {
		return nil, err
	}
	out, err := c.scope.Apply(ctx, u)
	if err != nil {
		return nil, err
	}
	return fromUnstructured[corev1.Secret](out)
}

// TypedResource provides typed CRUD over one cluster-scoped resource.
type TypedResource[T any, L any] struct {
	scope *tenant.Scope
	res   tenant.Resource
	gvk   schema.GroupVersionKind
}

func (r *TypedResource[T, L]) Get(ctx context.Context, name string, _ metav1.GetOptions) (*T, error) {
	u, err := r.scope.Get(ctx, r.res, "", name)
	if err != nil {
		return nil, err
	}
	return fromUnstructured[T](u)
}

func (r *TypedResource[T, L]) List(ctx context.Context, _ metav1.ListOptions) (*L, error) {
	items, err := r.scope.List(ctx, r.res, "")
	if err != nil {
		return nil, err
	}
	return fromUnstructuredList[L](&unstructured.UnstructuredList{Items: items})
}

func (r *TypedResource[T, L]) Create(ctx context.Context, obj *T, _ metav1.CreateOptions) (*T, error) {
	u, err := r.toUnstructured(obj)
	if err != nil {
		return nil, err
	}
	out, err := r.scope.Apply(ctx, u)
	if err != nil {
		return nil, err
	}
	return fromUnstructured[T](out)
}

func (r *TypedResource[T, L]) Update(ctx context.Context, obj *T, _ metav1.UpdateOptions) (*T, error) {
	u, err := r.toUnstructured(obj)
	if err != nil {
		return nil, err
	}
	out, err := r.scope.Apply(ctx, u)
	if err != nil {
		return nil, err
	}
	return fromUnstructured[T](out)
}

func (r *TypedResource[T, L]) UpdateStatus(ctx context.Context, obj *T, _ metav1.UpdateOptions) (*T, error) {
	u, err := r.toUnstructured(obj)
	if err != nil {
		return nil, err
	}
	if err := r.scope.ApplyStatus(ctx, u); err != nil {
		return nil, err
	}
	return obj, nil
}

func (r *TypedResource[T, L]) Delete(ctx context.Context, name string, _ metav1.DeleteOptions) error {
	return r.scope.Delete(ctx, r.res, "", name)
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

func toUnstructured(obj any) (*unstructured.Unstructured, error) {
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
	items := make([]any, 0, len(u.Items))
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
