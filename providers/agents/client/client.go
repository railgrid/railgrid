/*
Copyright 2026 The Railgrid Authors.

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
// provider's CRDs and to tenant Secrets, over the hub's kcp proxy. The
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

	agentsv1alpha1 "github.com/railgrid/provider-agents/apis/v1alpha1"
	"github.com/railgrid/provider-agents/tenant"
	"github.com/railgrid/provider-sdk/claimscope"
)

// ProviderName is this provider's CatalogEntry name. It is the value of the
// railgrid.ai/owner label on every Secret this provider owns, and therefore
// the value in its manifest's permission-claim selector; the two must agree or
// the provider writes Secrets it cannot read back.
const ProviderName = "agents"

// GVRs for the agents provider resources.
var (
	AgentGVR      = agentsGVR("agents")
	ConnectionGVR = agentsGVR("connections")
	ScheduleGVR   = agentsGVR("schedules")
	TriggerGVR    = agentsGVR("triggers")
	ToolsetGVR    = agentsGVR("toolsets")
	RunGVR        = agentsGVR("runs")
	// ModelCredentialGVR is the named model endpoint an agent runs on. The
	// key stays in the Secret spec.secretRef names; this object is what the
	// portal lists, what a reconciler validates, and what the probe verbs are
	// addressed at.
	ModelCredentialGVR = agentsGVR("modelcredentials")
	SecretGVR          = schema.GroupVersionResource{Group: "", Version: "v1", Resource: "secrets"}
)

func agentsGVR(resource string) schema.GroupVersionResource {
	return schema.GroupVersionResource{Group: agentsv1alpha1.GroupName, Version: agentsv1alpha1.Version, Resource: resource}
}

var (
	agentResource    = tenant.Resource{GVR: AgentGVR, Kind: "Agent", Plural: "Agents", Namespaced: false}
	connectionRes    = tenant.Resource{GVR: ConnectionGVR, Kind: "Connection", Plural: "Connections", Namespaced: false}
	scheduleResource = tenant.Resource{GVR: ScheduleGVR, Kind: "Schedule", Plural: "Schedules", Namespaced: false}
	triggerResource  = tenant.Resource{GVR: TriggerGVR, Kind: "Trigger", Plural: "Triggers", Namespaced: false}
	toolsetResource  = tenant.Resource{GVR: ToolsetGVR, Kind: "Toolset", Plural: "Toolsets", Namespaced: false}
	modelCredRes     = tenant.Resource{GVR: ModelCredentialGVR, Kind: "ModelCredential", Plural: "ModelCredentials", Namespaced: false}
	secretResource   = tenant.Resource{GVR: SecretGVR, Kind: "Secret", Plural: "Secrets", Namespaced: true}
)

// Client provides typed access to the agents provider's tenant-workspace
// resources through the hub's kcp proxy (see the tenant package).
type Client struct {
	scope *tenant.Scope
}

// NewFromScope builds a Client from a resolved tenant Scope.
func NewFromScope(scope *tenant.Scope) *Client {
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

// Toolsets returns a typed interface for Toolset resources.
func (c *Client) Toolsets() *TypedResource[agentsv1alpha1.Toolset, agentsv1alpha1.ToolsetList] {
	return &TypedResource[agentsv1alpha1.Toolset, agentsv1alpha1.ToolsetList]{
		scope: c.scope, res: toolsetResource,
		gvk: ToolsetGVR.GroupVersion().WithKind("Toolset"),
	}
}

// ModelCredentials returns a typed interface for ModelCredential resources.
func (c *Client) ModelCredentials() *TypedResource[agentsv1alpha1.ModelCredential, agentsv1alpha1.ModelCredentialList] {
	return &TypedResource[agentsv1alpha1.ModelCredential, agentsv1alpha1.ModelCredentialList]{
		scope: c.scope, res: modelCredRes,
		gvk: ModelCredentialGVR.GroupVersion().WithKind("ModelCredential"),
	}
}

// GetModelCredential reads one ModelCredential by name, which is what
// llm.CredentialResolver needs. It is spelled out rather than left to the
// caller so the whole provider resolves a credential name the same way.
func (c *Client) GetModelCredential(ctx context.Context, name string) (*agentsv1alpha1.ModelCredential, error) {
	return c.ModelCredentials().Get(ctx, name, metav1.GetOptions{})
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

// ApplySecret create-or-updates a Secret in the tenant workspace namespace,
// stamped as owned by this provider.
//
// The owner label is set here rather than at each call site because it is not
// decoration: this provider's `secrets` permission claim is scoped to it
// (manifest.yaml), so a Secret written without it is one the provider's own
// reconcilers and unattended runs can no longer see — kcp's APIExport virtual
// workspace filters LIST/WATCH by the claim and answers GET with a 404. And
// these writes go through the hub kcp proxy AS THE CALLER, not through the
// virtual workspace, so kcp's selector admission — which would have stamped it
// — never runs on them. Forgetting the label at one call site would therefore
// fail nowhere at write time and everywhere later.
func (c *Client) ApplySecret(ctx context.Context, s *corev1.Secret) (*corev1.Secret, error) {
	if s.APIVersion == "" {
		s.APIVersion = "v1"
	}
	if s.Kind == "" {
		s.Kind = "Secret"
	}
	s.Labels = claimscope.WithOwner(s.Labels, ProviderName)
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

// PatchStatus merge-patches the given status fields onto the named object's
// status subresource, leaving every other status field as it is. Use it for
// observed-state stamps (phase, lastRunAt) that must not clobber what other
// writers recorded, and that do not need a read first.
func (r *TypedResource[T, L]) PatchStatus(ctx context.Context, name string, status any) error {
	st, err := toUnstructured(status)
	if err != nil {
		return err
	}
	u := &unstructured.Unstructured{Object: map[string]any{}}
	u.SetAPIVersion(r.gvk.GroupVersion().String())
	u.SetKind(r.gvk.Kind)
	u.SetName(name)
	u.Object["status"] = st.Object
	return r.scope.ApplyStatus(ctx, u)
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
