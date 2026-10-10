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

package workloadidentity

import (
	"context"
	"fmt"
	"sort"
	"strings"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"

	tenancyv1alpha1 "github.com/railgrid/railgrid/apis/tenancy/v1alpha1"
	"github.com/railgrid/railgrid/pkg/hub/identity"
	"github.com/railgrid/railgrid/pkg/hub/serviceaccounts"
)

var projectGVR = schema.GroupVersionResource{
	Group: "ai.railgrid.ai", Version: "v1alpha1", Resource: "projects",
}

var _ identity.WorkloadScopeResolver = (*KCPProjectScopeResolver)(nil)

// ProjectScopeResolver verifies the request's Project object and derives the
// exact provider-resource references authorized by the selected environment.
// The exchange never accepts provider GVR/name data from the caller.
type ProjectScopeResolver interface {
	Resolve(context.Context, string, string, ExchangeRequest) (serviceaccounts.WorkloadIdentityScope, error)
}

// KCPProjectScopeResolver reads tenant Project objects through a child
// workspace config. The config builder is the same hub bootstrap seam used by
// ServiceAccount issuance, so no provider credential or static secret is
// introduced.
type KCPProjectScopeResolver struct {
	config serviceaccounts.WorkspaceConfigBuilder
	client dynamic.Interface
}

// NewKCPProjectScopeResolver constructs a KCP-backed resolver.
func NewKCPProjectScopeResolver(config serviceaccounts.WorkspaceConfigBuilder) *KCPProjectScopeResolver {
	return &KCPProjectScopeResolver{config: config}
}

// NewProjectScopeResolverForClient is a focused test/in-process seam. The
// production hub uses NewKCPProjectScopeResolver so each request targets the
// selected child workspace; this constructor never changes the verification
// rules and is useful for deterministic resolver tests.
func NewProjectScopeResolverForClient(client dynamic.Interface) *KCPProjectScopeResolver {
	return &KCPProjectScopeResolver{client: client}
}

// ResolveRecord implements identity.WorkloadScopeResolver. It rebuilds a
// recorded workload identity's current permissions from the exact project
// tuple captured when the identity was minted, allowing the ordinary hub
// identity sweep to apply integration revocations without waiting for a pod
// to exchange its token again.
func (r *KCPProjectScopeResolver) ResolveRecord(ctx context.Context, record *tenancyv1alpha1.ScopedIdentity) (serviceaccounts.WorkloadIdentityScope, error) {
	if record == nil || record.Spec.Attestation.Mode != tenancyv1alpha1.ScopedIdentityAttestationWorkload {
		return serviceaccounts.WorkloadIdentityScope{}, fmt.Errorf("workload identity record is required")
	}
	annotations := record.Spec.Annotations
	req := ExchangeRequest{
		TenantPath:  annotations[serviceaccounts.AnnotationWorkloadIdentityTenantPath],
		Project:     annotations[serviceaccounts.AnnotationWorkloadIdentityProject],
		ProjectUID:  annotations[serviceaccounts.AnnotationWorkloadIdentityProjectUID],
		Environment: annotations[serviceaccounts.AnnotationWorkloadIdentityEnvironment],
		Instance:    annotations[serviceaccounts.AnnotationWorkloadIdentityInstance],
	}
	orgUUID, wsUUID, err := parseTenantPath(req.TenantPath)
	if err != nil {
		return serviceaccounts.WorkloadIdentityScope{}, revokedScope(fmt.Sprintf("reading workload identity tenant path: %v", err))
	}
	return r.Resolve(ctx, orgUUID, wsUUID, req)
}

// Resolve verifies Project UID, environment, and instance membership, then
// returns providerReference (plus the matching owned instance binding) scopes.
func (r *KCPProjectScopeResolver) Resolve(ctx context.Context, orgUUID, wsUUID string, req ExchangeRequest) (serviceaccounts.WorkloadIdentityScope, error) {
	if r == nil || (r.config == nil && r.client == nil) {
		return serviceaccounts.WorkloadIdentityScope{}, fmt.Errorf("project scope resolver is unavailable")
	}
	if strings.TrimSpace(orgUUID) == "" || strings.TrimSpace(wsUUID) == "" {
		return serviceaccounts.WorkloadIdentityScope{}, revokedScope("tenant workspace is required")
	}
	if want := "root:railgrid:tenants:" + orgUUID + ":" + wsUUID; req.TenantPath != want {
		return serviceaccounts.WorkloadIdentityScope{}, revokedScope("tenantPath does not match selected workspace")
	}

	dyn := r.client
	var err error
	if dyn == nil {
		cfg := r.config.ChildWorkspaceConfig(orgUUID, wsUUID)
		if cfg == nil {
			return serviceaccounts.WorkloadIdentityScope{}, fmt.Errorf("tenant workspace config is unavailable")
		}
		dyn, err = dynamic.NewForConfig(cfg)
		if err != nil {
			return serviceaccounts.WorkloadIdentityScope{}, fmt.Errorf("building project client: %w", err)
		}
	}
	project, err := dyn.Resource(projectGVR).Get(ctx, req.Project, metav1.GetOptions{})
	if err != nil {
		if apierrors.IsNotFound(err) {
			return serviceaccounts.WorkloadIdentityScope{}, revokedScope("project is not found")
		}
		return serviceaccounts.WorkloadIdentityScope{}, fmt.Errorf("getting project: %w", err)
	}
	if string(project.GetUID()) == "" || string(project.GetUID()) != req.ProjectUID {
		return serviceaccounts.WorkloadIdentityScope{}, revokedScope("project UID does not match attested identity")
	}

	environments, found, err := unstructured.NestedSlice(project.Object, "spec", "environments")
	if err != nil || !found {
		return serviceaccounts.WorkloadIdentityScope{}, revokedScope("project environment is not declared")
	}
	var selected map[string]any
	for _, raw := range environments {
		environment, ok := raw.(map[string]any)
		if !ok {
			return serviceaccounts.WorkloadIdentityScope{}, revokedScope("project environments are malformed")
		}
		name, _, _ := unstructured.NestedString(environment, "name")
		if name == req.Environment {
			selected = environment
			break
		}
	}
	if selected == nil {
		return serviceaccounts.WorkloadIdentityScope{}, revokedScope("project environment does not match attested identity")
	}

	bindings, found, err := unstructured.NestedSlice(selected, "bindings")
	if err != nil {
		return serviceaccounts.WorkloadIdentityScope{}, revokedScope("project environment bindings are malformed")
	}
	if !found {
		bindings = nil
	}
	providerResources := make([]serviceaccounts.ProviderResourceScope, 0)
	instanceMatched := false
	integrationActions := false
	for _, raw := range bindings {
		binding, ok := raw.(map[string]any)
		if !ok {
			return serviceaccounts.WorkloadIdentityScope{}, revokedScope("project environment binding is malformed")
		}
		kind, _, _ := unstructured.NestedString(binding, "kind")
		bindingName, _, _ := unstructured.NestedString(binding, "name")
		providerName, _, _ := unstructured.NestedString(binding, "provider")
		ref, found, err := unstructured.NestedMap(binding, "resourceRef")
		if err != nil {
			return serviceaccounts.WorkloadIdentityScope{}, revokedScope("project provider resource reference is malformed")
		}
		if !found {
			if kind == "providerReference" {
				return serviceaccounts.WorkloadIdentityScope{}, revokedScope("providerReference has no resourceRef")
			}
			continue
		}
		resource, err := providerResourceScope(ref)
		if err != nil {
			return serviceaccounts.WorkloadIdentityScope{}, revokedScope(err.Error())
		}
		if kind == "providerReference" {
			actions, err := providerReferenceActions(binding)
			if err != nil {
				return serviceaccounts.WorkloadIdentityScope{}, revokedScope(err.Error())
			}
			resource.Actions = actions
			if len(actions) > 0 {
				integrationActions = true
			}
		}
		// The runtime instance must be the infrastructure-owned binding (or
		// App Studio's exact generated development binding). A providerReference
		// with a coincidentally identical name cannot satisfy this check.
		instanceBinding := kind == "providerResource" && resource.Name == req.Instance &&
			(providerName == "infrastructure" || (bindingName == "dev" && providerName == "app-studio"))
		if instanceBinding {
			instanceMatched = true
		}
		// Only active providerReference grants contribute a foreign resource
		// read/action rule. Revoked or actionless bindings must not leave an
		// access path behind. The matching providerResource is included so the
		// attested infrastructure instance remains reachable without a wildcard.
		if (kind == "providerReference" && len(resource.Actions) > 0) || instanceBinding {
			providerResources = append(providerResources, resource)
		}
	}
	if !instanceMatched {
		return serviceaccounts.WorkloadIdentityScope{}, revokedScope("instance does not belong to project environment")
	}
	providerResources = dedupeProviderResources(providerResources)
	return serviceaccounts.WorkloadIdentityScope{
		TenantPath: req.TenantPath, Project: req.Project, ProjectUID: req.ProjectUID,
		Environment: req.Environment, Instance: req.Instance, IntegrationActions: integrationActions,
		ProviderResources: providerResources,
	}, nil
}

// revokedScope identifies failures derived from the current Project contents
// (or corruption of the stored scope tuple). The identity sweep consumes this
// marker and tears down the old credential. API reads and client construction
// failures intentionally remain ordinary errors, preserving permissions
// across transient control-plane outages.
func revokedScope(reason string) error {
	return fmt.Errorf("%w: %s", identity.ErrWorkloadScopeRevoked, reason)
}

func providerResourceScope(ref map[string]any) (serviceaccounts.ProviderResourceScope, error) {
	apiVersion, _, _ := unstructured.NestedString(ref, "apiVersion")
	kind, _, _ := unstructured.NestedString(ref, "kind")
	resource, _, _ := unstructured.NestedString(ref, "resource")
	name, _, _ := unstructured.NestedString(ref, "name")
	if strings.TrimSpace(apiVersion) == "" || strings.TrimSpace(kind) == "" || strings.TrimSpace(resource) == "" || strings.TrimSpace(name) == "" {
		return serviceaccounts.ProviderResourceScope{}, fmt.Errorf("provider resource reference is incomplete")
	}
	return serviceaccounts.ProviderResourceScope{APIVersion: apiVersion, Kind: kind, Resource: resource, Name: name}, nil
}

// providerReferenceActions extracts the non-revoked action-grant names from a
// providerReference binding. These become create rules on the action's
// virtual subresource in the workload ClusterRole — the RBAC materialization
// of the Project grant. Version and schema digest stay in the grant record;
// RBAC carries the action family only.
func providerReferenceActions(binding map[string]any) ([]string, error) {
	raw, found, err := unstructured.NestedSlice(binding, "allowedActions")
	if err != nil {
		return nil, fmt.Errorf("project provider action grants are malformed")
	}
	if !found {
		return nil, nil
	}
	actions := make([]string, 0, len(raw))
	for _, entry := range raw {
		grant, ok := entry.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("project provider action grant is malformed")
		}
		revoked, _, _ := unstructured.NestedBool(grant, "revoked")
		if revoked {
			continue
		}
		name, _, _ := unstructured.NestedString(grant, "name")
		name = strings.ToLower(strings.TrimSpace(name))
		if name == "" {
			return nil, fmt.Errorf("project provider action grant has no name")
		}
		actions = append(actions, name)
	}
	sort.Strings(actions)
	return dedupeStrings(actions), nil
}

func dedupeStrings(values []string) []string {
	out := values[:0]
	var last string
	for i, value := range values {
		if i == 0 || value != last {
			out = append(out, value)
		}
		last = value
	}
	return out
}

func dedupeProviderResources(resources []serviceaccounts.ProviderResourceScope) []serviceaccounts.ProviderResourceScope {
	merged := make(map[string]*serviceaccounts.ProviderResourceScope, len(resources))
	order := make([]string, 0, len(resources))
	for _, resource := range resources {
		key := resource.APIVersion + "\x00" + resource.Resource + "\x00" + resource.Name
		if existing, ok := merged[key]; ok {
			// Same resource granted by more than one binding: union the
			// action grants so a duplicate reference cannot drop a grant.
			existing.Actions = dedupeStrings(sortedUnion(existing.Actions, resource.Actions))
			continue
		}
		copied := resource
		merged[key] = &copied
		order = append(order, key)
	}
	out := make([]serviceaccounts.ProviderResourceScope, 0, len(order))
	for _, key := range order {
		out = append(out, *merged[key])
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].APIVersion+"/"+out[i].Resource+"/"+out[i].Name < out[j].APIVersion+"/"+out[j].Resource+"/"+out[j].Name
	})
	return out
}

func sortedUnion(left, right []string) []string {
	union := append(append([]string(nil), left...), right...)
	sort.Strings(union)
	return union
}
