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

package identity

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strings"

	providersv1alpha1 "github.com/railgrid/railgrid/apis/providers/v1alpha1"
	tenancyv1alpha1 "github.com/railgrid/railgrid/apis/tenancy/v1alpha1"
	"github.com/railgrid/railgrid/pkg/apiurl"
	"github.com/railgrid/railgrid/pkg/hub/hubaccess"
	"github.com/railgrid/railgrid/pkg/hub/providers"
	"github.com/railgrid/railgrid/pkg/kcppaths"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/validation"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"
)

var (
	orgIdentityWorkspaceGVR      = schema.GroupVersionResource{Group: "tenancy.kcp.io", Version: "v1alpha1", Resource: "workspaces"}
	orgIdentityLogicalClusterGVR = schema.GroupVersionResource{Group: "core.kcp.io", Version: "v1alpha1", Resource: "logicalclusters"}
	orgIdentityOrganizationGVR   = schema.GroupVersionResource{Group: "tenancy.railgrid.ai", Version: "v1alpha1", Resource: "organizations"}
	orgIdentityBindingGVR        = schema.GroupVersionResource{Group: "apis.kcp.io", Version: "v1alpha2", Resource: "apibindings"}
	orgIdentityAPIExportGVR      = schema.GroupVersionResource{Group: "apis.kcp.io", Version: "v1alpha2", Resource: "apiexports"}
	orgIdentityCatalogEntryGVR   = schema.GroupVersionResource{Group: "providers.railgrid.ai", Version: "v1alpha1", Resource: "catalogentries"}
)

// OrgPolicyResolver re-resolves all org authority at each identity request.
// It must validate both the provider source and target workspace before
// returning a policy; callers must not cache its result across requests.
type OrgPolicyResolver interface {
	Resolve(ctx context.Context, orgUUID, provider, clusterID string) (*Policy, error)
}

// OrgIdentityPolicyResolver builds a fail-closed identity policy from live
// kcp state. Its clients use hub credentials and never a provider-supplied
// kubeconfig.
type OrgIdentityPolicyResolver struct {
	registry  *providers.Registry
	grants    hubaccess.UncachedGrantReader
	clientFor func(path string) (dynamic.Interface, error)
}

// NewOrgIdentityPolicyResolver builds the resolver over the hub's kcp
// credential and the uncached grant reader used by the request authorization
// boundary.
func NewOrgIdentityPolicyResolver(config *rest.Config, registry *providers.Registry, grants hubaccess.UncachedGrantReader) (*OrgIdentityPolicyResolver, error) {
	if config == nil || config.Host == "" || registry == nil || grants == nil {
		return nil, fmt.Errorf("organization identity policy requires kcp, provider registry, and uncached grants")
	}
	trusted := rest.CopyConfig(config)
	return &OrgIdentityPolicyResolver{
		registry: registry,
		grants:   grants,
		clientFor: func(path string) (dynamic.Interface, error) {
			cfg := rest.CopyConfig(trusted)
			cfg.Host = apiurl.KCPClusterURL(cfg.Host, path)
			return dynamic.NewForConfig(cfg)
		},
	}, nil
}

// NewOrgIdentityPolicyResolverForClients is the test seam for live-scope
// checks. It retains the same policy and validation logic.
func NewOrgIdentityPolicyResolverForClients(registry *providers.Registry, grants hubaccess.UncachedGrantReader, clientFor func(string) (dynamic.Interface, error)) *OrgIdentityPolicyResolver {
	return &OrgIdentityPolicyResolver{registry: registry, grants: grants, clientFor: clientFor}
}

// Resolve verifies the requesting org-owned provider, its current source,
// the active target org/team workspace, its exact current binding, and fresh
// composition consent before building the per-request policy.
func (r *OrgIdentityPolicyResolver) Resolve(ctx context.Context, orgUUID, providerName, clusterID string) (*Policy, error) {
	deny := func() (*Policy, error) {
		return nil, Refusal{Code: CodeUnboundProvider, Reason: "the provider identity request is not authorized in this workspace"}
	}
	if r == nil || r.registry == nil || r.grants == nil || r.clientFor == nil ||
		len(validation.IsDNS1123Label(orgUUID)) != 0 || len(validation.IsDNS1123Label(providerName)) != 0 ||
		len(validation.IsDNS1123Label(clusterID)) != 0 {
		return deny()
	}

	requester, ok := r.registry.GetForOrg(orgUUID, providerName)
	if !ok || requester.OrgUUID != orgUUID || requester.APIExportPath != kcppaths.OrgProviderPath(orgUUID, providerName) ||
		requester.APIExportName == "" || len(requester.APIGroups) == 0 {
		return deny()
	}
	current, ok := r.registry.GetForOrg(orgUUID, providerName)
	if !ok || current.OrgUUID != orgUUID || current.APIExportPath != requester.APIExportPath || current.APIExportName != requester.APIExportName ||
		!sameStrings(current.APIGroups, requester.APIGroups) || !reflect.DeepEqual(current.Requires, requester.Requires) ||
		!reflect.DeepEqual(current.Export, requester.Export) {
		return deny()
	}
	sourceSurface, err := r.verifyProviderSourceSurface(ctx, orgUUID, requester)
	if err != nil {
		if isScopeDenied(err) {
			return deny()
		}
		return nil, fmt.Errorf("verifying organization provider source: %w", err)
	}

	workspaceUUID, err := r.verifyTargetWorkspace(ctx, orgUUID, clusterID)
	if err != nil {
		if isScopeDenied(err) {
			return deny()
		}
		return nil, fmt.Errorf("verifying organization identity target: %w", err)
	}

	target, err := r.clientFor(clusterID)
	if err != nil {
		return nil, fmt.Errorf("creating organization identity target client: %w", err)
	}
	bindings, err := target.Resource(orgIdentityBindingGVR).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("listing target APIBindings: %w", err)
	}
	if !hasExactBoundExport(bindings.Items, requester.APIExportPath, requester.APIExportName) {
		return deny()
	}

	grant, err := r.grants.GetUncached(ctx, hubaccess.GrantKey{
		OrgUUID: orgUUID, WorkspaceUUID: workspaceUUID,
		Provider: requester.Name, ProviderOrgUUID: orgUUID,
	})
	if err != nil {
		return nil, fmt.Errorf("reading current provider composition consent: %w", err)
	}

	consented := acceptedAppliedClaims(findExactBoundExport(bindings.Items, requester.APIExportPath, requester.APIExportName))
	catalog := orgIdentityCatalog{
		registry: r.registry, orgUUID: orgUUID, requester: requester, dependencies: map[string]map[string]bool{},
		ownResources: sourceSurface.resources,
	}
	for _, coordinate := range providersv1alpha1.RequiredCoordinates(requester.Requires) {
		if coordinate.Provider == "" || coordinate.Group == "" || coordinate.Selector != nil {
			continue
		}
		dep, found := r.registry.GetForOrg(orgUUID, coordinate.Provider)
		if !found || !containsAny(dep.APIGroups, coordinate.Group) {
			continue
		}
		depSurface, err := r.verifyProviderExportSurface(ctx, orgUUID, dep)
		if err != nil {
			continue
		}
		if !depSurface.has(coordinate.Group, coordinate.Resource) {
			continue
		}
		claimVerbs := consented[coordinate.Group+"/"+coordinate.Resource]
		claimAllowed := claimVerbs != nil
		if coordinate.Coordinate {
			claimAllowed = claimAllowed && claimVerbs["*"]
		} else {
			claimAllowed = claimAllowed && claimVerbs["get"]
		}
		if !hasExactBoundExport(bindings.Items, dep.APIExportPath, dep.APIExportName) ||
			!compositionAccepted(grant, coordinate.Group, coordinate.Resource) ||
			!claimAllowed {
			continue
		}
		if catalog.dependencies[coordinate.Group] == nil {
			catalog.dependencies[coordinate.Group] = map[string]bool{}
		}
		catalog.dependencies[coordinate.Group][coordinate.Resource] = true
	}
	return NewPolicy(catalog, orgIdentityBindings{registry: r.registry, orgUUID: orgUUID, bindings: bindings.Items}), nil
}

func (r *OrgIdentityPolicyResolver) verifyProviderExportSurface(ctx context.Context, orgUUID string, provider providers.Provider) (providerExportSurface, error) {
	if provider.OrgUUID != "" {
		return r.verifyProviderSourceSurface(ctx, orgUUID, provider)
	}
	return r.verifyLiveExport(ctx, provider)
}

func (r *OrgIdentityPolicyResolver) verifyProviderSource(ctx context.Context, orgUUID string, provider providers.Provider) error {
	_, err := r.verifyProviderSourceSurface(ctx, orgUUID, provider)
	return err
}

func (r *OrgIdentityPolicyResolver) verifyProviderSourceSurface(ctx context.Context, orgUUID string, provider providers.Provider) (providerExportSurface, error) {
	if provider.APIExportPath != kcppaths.OrgProviderPath(orgUUID, provider.Name) || provider.APIExportName == "" {
		return providerExportSurface{}, scopeDenied()
	}
	parent, err := r.clientFor(kcppaths.OrgProvidersParent(orgUUID))
	if err != nil {
		return providerExportSurface{}, err
	}
	workspace, err := parent.Resource(orgIdentityWorkspaceGVR).Get(ctx, provider.Name, metav1.GetOptions{})
	if err != nil {
		return providerExportSurface{}, err
	}
	cluster, _, _ := unstructured.NestedString(workspace.Object, "spec", "cluster")
	phase, _, _ := unstructured.NestedString(workspace.Object, "status", "phase")
	if workspace.GetDeletionTimestamp() != nil || workspace.GetAnnotations()[providers.OrgProviderBootstrapPolicyAnnotation] != providers.OrgProviderBootstrapPolicy ||
		phase != "Ready" || len(validation.IsDNS1123Label(cluster)) != 0 ||
		provider.CatalogEntryCluster != cluster || provider.WorkspaceCluster != "" && provider.WorkspaceCluster != cluster {
		return providerExportSurface{}, scopeDenied()
	}
	source, err := r.clientFor(provider.APIExportPath)
	if err != nil {
		return providerExportSurface{}, err
	}
	entry, err := source.Resource(orgIdentityCatalogEntryGVR).Get(ctx, provider.Name, metav1.GetOptions{})
	if err != nil {
		return providerExportSurface{}, err
	}
	entryExportName, _, _ := unstructured.NestedString(entry.Object, "spec", "export", "name")
	if entry.GetDeletionTimestamp() != nil || entryExportName != provider.APIExportName {
		return providerExportSurface{}, scopeDenied()
	}
	var liveContract struct {
		Export   *providersv1alpha1.ProviderExport       `json:"export,omitempty"`
		Requires []providersv1alpha1.ProviderRequirement `json:"requires,omitempty"`
	}
	spec, found, _ := unstructured.NestedMap(entry.Object, "spec")
	if !found {
		return providerExportSurface{}, fmt.Errorf("catalog entry has no current spec")
	}
	if err := runtime.DefaultUnstructuredConverter.FromUnstructured(spec, &liveContract); err != nil {
		return providerExportSurface{}, fmt.Errorf("decoding current provider contract: %w", err)
	}
	if !reflect.DeepEqual(liveContract.Requires, provider.Requires) || !reflect.DeepEqual(liveContract.Export, provider.Export) {
		return providerExportSurface{}, scopeDenied()
	}
	surface, err := r.verifyLiveExport(ctx, provider)
	if err != nil {
		return providerExportSurface{}, err
	}
	latest, ok := r.registry.GetForOrg(orgUUID, provider.Name)
	if !ok || latest.OrgUUID != orgUUID || latest.APIExportPath != provider.APIExportPath || latest.APIExportName != provider.APIExportName ||
		latest.CatalogEntryCluster != cluster || latest.WorkspaceCluster != "" && latest.WorkspaceCluster != cluster ||
		!sameStrings(latest.APIGroups, provider.APIGroups) || !reflect.DeepEqual(latest.Requires, provider.Requires) ||
		!reflect.DeepEqual(latest.Export, provider.Export) {
		return providerExportSurface{}, scopeDenied()
	}
	return surface, nil
}

func (r *OrgIdentityPolicyResolver) verifyLiveExport(ctx context.Context, provider providers.Provider) (providerExportSurface, error) {
	if provider.APIExportPath == "" || provider.APIExportName == "" {
		return providerExportSurface{}, scopeDenied()
	}
	source, err := r.clientFor(provider.APIExportPath)
	if err != nil {
		return providerExportSurface{}, err
	}
	apiExport, err := source.Resource(orgIdentityAPIExportGVR).Get(ctx, provider.APIExportName, metav1.GetOptions{})
	if err != nil {
		return providerExportSurface{}, err
	}
	surface := exportedSurface(apiExport)
	if apiExport.GetDeletionTimestamp() != nil || !sameStrings(surface.groups, provider.APIGroups) {
		return providerExportSurface{}, scopeDenied()
	}
	return surface, nil
}

func (r *OrgIdentityPolicyResolver) verifyTargetWorkspace(ctx context.Context, orgUUID, clusterID string) (string, error) {
	organizationClient, err := r.clientFor(kcppaths.SystemTenants)
	if err != nil {
		return "", err
	}
	organization, err := organizationClient.Resource(orgIdentityOrganizationGVR).Get(ctx, orgUUID, metav1.GetOptions{})
	if err != nil {
		return "", err
	}
	deletionRequestedAt, _, _ := unstructured.NestedString(organization.Object, "status", "deletionRequestedAt")
	if organization.GetDeletionTimestamp() != nil || deletionRequestedAt != "" || !hasReadyCondition(organization) {
		return "", scopeDenied()
	}

	tenant, err := r.clientFor(kcppaths.TenantsParent)
	if err != nil {
		return "", err
	}
	orgWorkspace, err := tenant.Resource(orgIdentityWorkspaceGVR).Get(ctx, orgUUID, metav1.GetOptions{})
	if err != nil {
		return "", err
	}
	orgCluster, _, _ := unstructured.NestedString(orgWorkspace.Object, "spec", "cluster")
	orgType, _, _ := unstructured.NestedString(orgWorkspace.Object, "spec", "type", "name")
	orgPhase, _, _ := unstructured.NestedString(orgWorkspace.Object, "status", "phase")
	if orgWorkspace.GetDeletionTimestamp() != nil || orgType != "organization" || orgPhase != "Ready" || len(validation.IsDNS1123Label(orgCluster)) != 0 {
		return "", scopeDenied()
	}

	target, err := r.clientFor(clusterID)
	if err != nil {
		return "", err
	}
	lc, err := target.Resource(orgIdentityLogicalClusterGVR).Get(ctx, "cluster", metav1.GetOptions{})
	if err != nil {
		return "", err
	}
	path := lc.GetAnnotations()["kcp.io/path"]
	annotatedCluster := lc.GetAnnotations()["kcp.io/cluster"]
	pathOrg, workspaceUUID, ok := kcppaths.SplitWorkspacePath(path)
	if !ok || pathOrg != orgUUID || annotatedCluster != clusterID || lc.GetDeletionTimestamp() != nil || !kcppaths.IsTeamWorkspaceName(workspaceUUID) {
		return "", scopeDenied()
	}

	orgClient, err := r.clientFor(kcppaths.OrgPath(orgUUID))
	if err != nil {
		return "", err
	}
	workspace, err := orgClient.Resource(orgIdentityWorkspaceGVR).Get(ctx, workspaceUUID, metav1.GetOptions{})
	if err != nil {
		return "", err
	}
	workspaceCluster, _, _ := unstructured.NestedString(workspace.Object, "spec", "cluster")
	workspaceType, _, _ := unstructured.NestedString(workspace.Object, "spec", "type", "name")
	workspacePhase, _, _ := unstructured.NestedString(workspace.Object, "status", "phase")
	if workspace.GetDeletionTimestamp() != nil || workspaceType != "workspace" || workspacePhase != "Ready" || workspaceCluster != clusterID {
		return "", scopeDenied()
	}
	return workspaceUUID, nil
}

func hasReadyCondition(object *unstructured.Unstructured) bool {
	conditions, _, _ := unstructured.NestedSlice(object.Object, "status", "conditions")
	for _, raw := range conditions {
		condition, ok := raw.(map[string]any)
		if ok && condition["type"] == tenancyv1alpha1.OrganizationConditionReady && condition["status"] == "True" {
			return true
		}
	}
	return false
}

func isScopeDenied(err error) bool {
	var refusal Refusal
	return apierrors.IsNotFound(err) || errors.As(err, &refusal)
}

func scopeDenied() error {
	return Refusal{Code: CodeUnboundProvider, Reason: "the provider identity request is not authorized in this workspace"}
}

type providerExportSurface struct {
	groups    []string
	resources map[string]map[string]bool
}

func (s providerExportSurface) has(group, resource string) bool {
	return s.resources[group][resource]
}

func exportedSurface(apiExport *unstructured.Unstructured) providerExportSurface {
	surface := providerExportSurface{resources: map[string]map[string]bool{}}
	if apiExport == nil {
		return surface
	}
	resources, _, _ := unstructured.NestedSlice(apiExport.Object, "spec", "resources")
	groups := map[string]bool{}
	for _, raw := range resources {
		resource, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		group, _ := resource["group"].(string)
		name, _ := resource["name"].(string)
		if group != "" {
			groups[group] = true
		}
		if group == "" || name == "" {
			continue
		}
		if surface.resources[group] == nil {
			surface.resources[group] = map[string]bool{}
		}
		surface.resources[group][name] = true
		// Status is part of every exported resource's ordinary API surface;
		// it is not repeated as a separate custom coordinate on an APIExport.
		if !strings.Contains(name, "/") {
			surface.resources[group][name+"/status"] = true
		}
	}
	surface.groups = make([]string, 0, len(groups))
	for group := range groups {
		surface.groups = append(surface.groups, group)
	}
	sort.Strings(surface.groups)
	return surface
}

func sameStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func findExactBoundExport(bindings []unstructured.Unstructured, path, name string) *unstructured.Unstructured {
	for i := range bindings {
		bindingPath, _, _ := unstructured.NestedString(bindings[i].Object, "spec", "reference", "export", "path")
		bindingName, _, _ := unstructured.NestedString(bindings[i].Object, "spec", "reference", "export", "name")
		if bindingPath == path && bindingName == name {
			return &bindings[i]
		}
	}
	return nil
}

func hasExactBoundExport(bindings []unstructured.Unstructured, path, name string) bool {
	if path == "" || name == "" {
		return false
	}
	matches := 0
	for i := range bindings {
		bindingPath, _, _ := unstructured.NestedString(bindings[i].Object, "spec", "reference", "export", "path")
		bindingName, _, _ := unstructured.NestedString(bindings[i].Object, "spec", "reference", "export", "name")
		if bindingPath == path || bindingName == name {
			if bindingPath != path || bindingName != name {
				return false
			}
			phase, _, _ := unstructured.NestedString(bindings[i].Object, "status", "phase")
			if phase != "Bound" {
				return false
			}
			if hasInvalidPermissionClaimsCondition(&bindings[i]) {
				return false
			}
			matches++
		}
	}
	return matches == 1
}

func hasInvalidPermissionClaimsCondition(binding *unstructured.Unstructured) bool {
	conditions, _, _ := unstructured.NestedSlice(binding.Object, "status", "conditions")
	for _, raw := range conditions {
		condition, ok := raw.(map[string]any)
		if ok && condition["type"] == "PermissionClaimsValid" && condition["status"] == "False" {
			return true
		}
	}
	return false
}

// acceptedAppliedClaims is an intersection, not an interpretation of only
// the user-authored binding spec. A dependency is usable only when its claim
// remains declared by the export and kcp reports it applied.
func acceptedAppliedClaims(binding *unstructured.Unstructured) map[string]map[string]bool {
	out := map[string]map[string]bool{}
	if binding == nil {
		return out
	}
	spec, _, _ := unstructured.NestedSlice(binding.Object, "spec", "permissionClaims")
	applied, _, _ := unstructured.NestedSlice(binding.Object, "status", "appliedPermissionClaims")
	exported, _, _ := unstructured.NestedSlice(binding.Object, "status", "exportPermissionClaims")
	accepted := claimIndex(spec, true)
	active := claimIndex(applied, false)
	current := claimIndex(exported, false)
	for key, specClaim := range accepted {
		activeClaim, activeOK := active[key]
		currentClaim, currentOK := current[key]
		if !activeOK || !currentOK || hasClaimSelector(specClaim) || hasClaimSelector(activeClaim) || hasClaimSelector(currentClaim) ||
			claimHasIdentityHash(specClaim) || claimHasIdentityHash(activeClaim) || claimHasIdentityHash(currentClaim) {
			continue
		}
		verbs := stringSet(specClaim["verbs"])
		intersectInto(verbs, stringSet(activeClaim["verbs"]))
		intersectInto(verbs, stringSet(currentClaim["verbs"]))
		if len(verbs) > 0 {
			out[key] = verbs
		}
	}
	return out
}

func claimIndex(raw []any, requireAccepted bool) map[string]map[string]any {
	out := map[string]map[string]any{}
	duplicates := map[string]bool{}
	for _, item := range raw {
		claim, ok := item.(map[string]any)
		if !ok {
			continue
		}
		group, _ := claim["group"].(string)
		resource, _ := claim["resource"].(string)
		if resource == "" {
			continue
		}
		key := group + "/" + resource
		if duplicates[key] {
			continue
		}
		if _, duplicate := out[key]; duplicate {
			delete(out, key)
			duplicates[key] = true
			continue
		}
		out[key] = claim
	}
	for key := range duplicates {
		delete(out, key)
	}
	if requireAccepted {
		for key, claim := range out {
			if claim["state"] != "Accepted" {
				delete(out, key)
			}
		}
	}
	return out
}

func stringSet(raw any) map[string]bool {
	items, ok := raw.([]any)
	if !ok || len(items) == 0 {
		return nil
	}
	out := map[string]bool{}
	for _, item := range items {
		value, ok := item.(string)
		if !ok || value == "" {
			return nil
		}
		out[value] = true
	}
	return out
}

func intersectInto(target, other map[string]bool) {
	for value := range target {
		if !other[value] {
			delete(target, value)
		}
	}
}

func hasClaimSelector(claim map[string]any) bool {
	selector, exists := claim["selector"]
	if !exists || selector == nil {
		return false
	}
	return true
}

func claimHasIdentityHash(claim map[string]any) bool {
	identity, exists := claim["identityHash"]
	return exists && identity != ""
}

func compositionAccepted(grant *tenancyv1alpha1.Grant, group, resource string) bool {
	return hubaccess.DecideComposition(grant, hubaccess.CompositionRequirement{Group: group, Resource: resource}) == hubaccess.Accepted
}

type orgIdentityCatalog struct {
	registry     *providers.Registry
	orgUUID      string
	requester    providers.Provider
	dependencies map[string]map[string]bool
	ownResources map[string]map[string]bool
}

func (c orgIdentityCatalog) GroupOwner(group string) (string, bool) {
	for _, requirement := range c.requester.Requires {
		if requirement.Provider == "" || requirement.Group != group {
			continue
		}
		provider, ok := c.registry.GetForOrg(c.orgUUID, requirement.Provider)
		if ok && containsAny(provider.APIGroups, group) {
			return provider.Name, true
		}
	}
	return "", false
}

func (c orgIdentityCatalog) ExportedGroups(provider string) []string {
	if provider != c.requester.Name {
		return nil
	}
	return append([]string(nil), c.requester.APIGroups...)
}

func (c orgIdentityCatalog) DeclaredVerbs(providerName, resource string) []string {
	provider, ok := c.registry.GetForOrg(c.orgUUID, providerName)
	if !ok {
		return nil
	}
	return dedupeSorted(providersv1alpha1.ProviderDeclaredVerbs(provider.Export, resource))
}

func (c orgIdentityCatalog) AllowsForeignRule(requester, providerName, group string, resources []string) bool {
	if requester != c.requester.Name || c.dependencies[group] == nil {
		return false
	}
	for _, resource := range resources {
		if strings.Contains(resource, "*") || !c.dependencies[group][resource] {
			return false
		}
	}
	return true
}

func (c orgIdentityCatalog) AllowsOwnRule(requester, group string, resources []string) bool {
	if requester != c.requester.Name || c.ownResources[group] == nil {
		return false
	}
	// Bootstrap checks this when the declaration arrives, but registry
	// ownership can change afterwards (for example when a platform provider's
	// APIExport becomes readable). Re-check at mint time so a provider cannot
	// use clause A to take write verbs on a differently named provider's group.
	if c.registry != nil {
		if _, foreign := c.registry.ForeignGroupOwner(c.orgUUID, c.requester.Name, group); foreign {
			return false
		}
	}
	for _, resource := range resources {
		if !c.ownResources[group][resource] {
			return false
		}
	}
	return true
}

type orgIdentityBindings struct {
	registry *providers.Registry
	orgUUID  string
	bindings []unstructured.Unstructured
}

func (b orgIdentityBindings) IsBound(_ string, providerName string) (bool, error) {
	provider, ok := b.registry.GetForOrg(b.orgUUID, providerName)
	if !ok || provider.OrgUUID != "" && provider.OrgUUID != b.orgUUID {
		return false, nil
	}
	return hasExactBoundExport(b.bindings, provider.APIExportPath, provider.APIExportName), nil
}
