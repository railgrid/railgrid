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
	"testing"

	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/dynamic/fake"

	providersv1alpha1 "github.com/railgrid/railgrid/apis/providers/v1alpha1"
	tenancyv1alpha1 "github.com/railgrid/railgrid/apis/tenancy/v1alpha1"
	"github.com/railgrid/railgrid/pkg/hub/hubaccess"
	"github.com/railgrid/railgrid/pkg/hub/providers"
	"github.com/railgrid/railgrid/pkg/kcppaths"
)

type testOrgGrantReader struct {
	grant *tenancyv1alpha1.Grant
	err   error
}

func (g *testOrgGrantReader) GetUncached(context.Context, hubaccess.GrantKey) (*tenancyv1alpha1.Grant, error) {
	return g.grant, g.err
}

type orgScopeFixture struct {
	resolver *OrgIdentityPolicyResolver
	clients  map[string]dynamic.Interface
	grants   *testOrgGrantReader
	registry *providers.Registry
}

func newOrgScopeFixture(t *testing.T, targetPath string, dependencyConsent bool) *orgScopeFixture {
	t.Helper()
	const (
		orgUUID          = "org-a"
		factory          = "factory"
		factoryExport    = "factory.providers.railgrid.ai"
		factoryGroup     = "factory.example"
		providerCluster  = "provider-cluster"
		teamCluster      = "team-cluster"
		dependency       = "databricks"
		dependencyExport = "databricks.providers.railgrid.ai"
		dependencyGroup  = "databricks.example"
	)

	reg := providers.NewRegistry()
	requirements := []providersv1alpha1.ProviderRequirement(nil)
	factoryClaims := []any(nil)
	var grant *tenancyv1alpha1.Grant
	if dependencyConsent {
		requirements = []providersv1alpha1.ProviderRequirement{{
			Provider: dependency,
			Group:    dependencyGroup,
			Resources: []providersv1alpha1.ProviderRequiredResource{{
				Name:  "tables",
				Verbs: []providersv1alpha1.ProviderRequiredVerb{providersv1alpha1.RequiredVerbGet},
			}},
		}}
		factoryClaims = []any{map[string]any{"group": dependencyGroup, "resource": "tables", "verbs": []any{"get"}, "state": "Accepted"}}
		grant = &tenancyv1alpha1.Grant{Spec: tenancyv1alpha1.GrantSpec{
			Subject: tenancyv1alpha1.GrantSubject{Kind: tenancyv1alpha1.GrantSubjectProvider, Name: factory, OrgUUID: orgUUID},
			OrgUUID: orgUUID, WorkspaceUUID: "team-a",
			Capabilities: []tenancyv1alpha1.GrantedCapability{{Capability: hubaccess.ComposeCapability(dependencyGroup, "tables"), Scope: "workspace"}},
		}}
	}

	factoryPath := kcppaths.OrgProviderPath(orgUUID, factory)
	reg.Upsert(providers.Provider{
		Name: factory, OrgUUID: orgUUID, APIExportPath: factoryPath, APIExportName: factoryExport,
		APIGroups: []string{factoryGroup}, WorkspaceCluster: providerCluster, CatalogEntryCluster: providerCluster,
		Export: &providersv1alpha1.ProviderExport{Name: factoryExport}, Requires: requirements,
	})
	if dependencyConsent {
		reg.Upsert(providers.Provider{
			Name: dependency, APIExportPath: kcppaths.ProviderPath(dependency), APIExportName: dependencyExport,
			APIGroups: []string{dependencyGroup}, WorkspaceCluster: "provider-cluster-databricks",
			Export: &providersv1alpha1.ProviderExport{Name: dependencyExport},
		})
	}

	providerWorkspace := testUnstructured("tenancy.kcp.io/v1alpha1", "Workspace", "factory",
		map[string]any{"providers.railgrid.ai/bootstrap-policy": "restricted-v1"},
		map[string]any{"cluster": providerCluster}, map[string]any{"phase": "Ready"})
	factoryRequires := map[string]any{}
	if dependencyConsent {
		factoryRequires["requires"] = []any{map[string]any{
			"provider": dependency,
			"group":    dependencyGroup,
			"resources": []any{map[string]any{
				"name": "tables", "verbs": []any{"get"},
			}},
		}}
	}
	entry := testUnstructured("providers.railgrid.ai/v1alpha1", "CatalogEntry", "factory", nil,
		map[string]any{"export": map[string]any{"name": factoryExport}}, nil)
	entrySpec := entry.Object["spec"].(map[string]any)
	for key, value := range factoryRequires {
		entrySpec[key] = value
	}
	factoryExportObject := testUnstructured("apis.kcp.io/v1alpha2", "APIExport", factoryExport, nil,
		map[string]any{"resources": []any{map[string]any{"group": factoryGroup, "name": "runs"}}}, nil)
	sourceObjects := []runtime.Object{entry, factoryExportObject}
	if dependencyConsent {
		sourceObjects = append(sourceObjects, testUnstructured("apis.kcp.io/v1alpha2", "APIExport", dependencyExport, nil,
			map[string]any{"resources": []any{map[string]any{"group": dependencyGroup, "name": "tables"}}}, nil))
	}
	providerSource := testDynamicClient(sourceObjects...)
	providerParent := testDynamicClient(providerWorkspace)
	orgWorkspace := testUnstructured("tenancy.kcp.io/v1alpha1", "Workspace", orgUUID, nil,
		map[string]any{"cluster": "org-cluster", "type": map[string]any{"name": "organization"}}, map[string]any{"phase": "Ready"})
	organization := testUnstructured(tenancyv1alpha1.SchemeGroupVersion.String(), "Organization", orgUUID, nil, nil,
		map[string]any{"conditions": []any{map[string]any{"type": "Ready", "status": "True"}}})
	tenants := testDynamicClient(orgWorkspace, organization)
	teamWorkspace := testUnstructured("tenancy.kcp.io/v1alpha1", "Workspace", "team-a", nil,
		map[string]any{"cluster": teamCluster, "type": map[string]any{"name": "workspace"}}, map[string]any{"phase": "Ready"})
	org := testDynamicClient(teamWorkspace)
	logicalCluster := testUnstructured("core.kcp.io/v1alpha1", "LogicalCluster", "cluster",
		map[string]any{"kcp.io/path": targetPath, "kcp.io/cluster": teamCluster}, nil, nil)
	bindings := []runtime.Object{testBinding("factory", factoryPath, factoryExport, factoryClaims)}
	if dependencyConsent {
		bindings = append(bindings, testBinding(dependency, kcppaths.ProviderPath(dependency), dependencyExport, nil))
	}
	target := testDynamicClient(append([]runtime.Object{logicalCluster}, bindings...)...)

	clients := map[string]dynamic.Interface{
		kcppaths.OrgProvidersParent(orgUUID): providerParent,
		factoryPath:                          providerSource,
		kcppaths.TenantsParent:               tenants,
		kcppaths.SystemTenants:               tenants,
		kcppaths.OrgPath(orgUUID):            org,
		teamCluster:                          target,
		kcppaths.ProviderPath(dependency):    providerSource,
	}
	grantReader := &testOrgGrantReader{grant: grant}
	resolver := NewOrgIdentityPolicyResolverForClients(reg, grantReader, func(path string) (dynamic.Interface, error) {
		client := clients[path]
		if client == nil {
			t.Fatalf("unexpected client path %q", path)
		}
		return client, nil
	})
	return &orgScopeFixture{resolver: resolver, clients: clients, grants: grantReader, registry: reg}
}

func testUnstructured(apiVersion, kind, name string, annotations, spec, status map[string]any) *unstructured.Unstructured {
	object := map[string]any{
		"apiVersion": apiVersion,
		"kind":       kind,
		"metadata":   map[string]any{"name": name},
	}
	metadata := object["metadata"].(map[string]any)
	if annotations != nil {
		metadata["annotations"] = annotations
	}
	if spec != nil {
		object["spec"] = spec
	}
	if status != nil {
		object["status"] = status
	}
	return &unstructured.Unstructured{Object: object}
}

func testDynamicClient(objects ...runtime.Object) *fake.FakeDynamicClient {
	return fake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{
		orgIdentityBindingGVR: "APIBindingList",
	}, objects...)
}

func testBinding(name, path, exportName string, claims []any) *unstructured.Unstructured {
	spec := map[string]any{"reference": map[string]any{"export": map[string]any{"path": path, "name": exportName}}}
	if claims != nil {
		spec["permissionClaims"] = claims
	}
	status := map[string]any{"phase": "Bound", "apiExportClusterName": "provider-cluster-" + name}
	if claims != nil {
		status["appliedPermissionClaims"] = []any{map[string]any{"group": "databricks.example", "resource": "tables", "verbs": []any{"get"}}}
		status["exportPermissionClaims"] = []any{map[string]any{"group": "databricks.example", "resource": "tables", "verbs": []any{"get"}}}
	}
	return testUnstructured("apis.kcp.io/v1alpha2", "APIBinding", name, nil, spec, status)
}

func TestOrgIdentityPolicyRequiresAttestedOrgTargetAndExactBinding(t *testing.T) {
	t.Run("valid", func(t *testing.T) {
		fixture := newOrgScopeFixture(t, kcppaths.WorkspacePath("org-a", "team-a"), false)
		requester, _ := fixture.registry.GetForOrg("org-a", "factory")
		if err := fixture.resolver.verifyProviderSource(context.Background(), "org-a", requester); err != nil {
			t.Fatalf("verifyProviderSource: %v", err)
		}
		if _, err := fixture.resolver.verifyTargetWorkspace(context.Background(), "org-a", "team-cluster"); err != nil {
			t.Fatalf("verifyTargetWorkspace: %v", err)
		}
		policy, err := fixture.resolver.Resolve(context.Background(), "org-a", "factory", "team-cluster")
		if err != nil {
			t.Fatalf("Resolve: %v", err)
		}
		if _, err := policy.Authorize("factory", "team-cluster", []rbacv1.PolicyRule{{
			APIGroups: []string{"factory.example"}, Resources: []string{"runs"}, Verbs: []string{"get"}, ResourceNames: []string{"run-a"},
		}}); err != nil {
			t.Fatalf("own group rule was refused: %v", err)
		}
		if _, err := policy.Authorize("factory", "team-cluster", []rbacv1.PolicyRule{{
			APIGroups: []string{"factory.example"}, Resources: []string{"runs/status"}, Verbs: []string{"update"}, ResourceNames: []string{"run-a"},
		}}); err != nil {
			t.Fatalf("status on an exported resource was refused: %v", err)
		}
		_, err = policy.Authorize("factory", "team-cluster", []rbacv1.PolicyRule{{
			APIGroups: []string{"factory.example"}, Resources: []string{"runs/future"}, Verbs: []string{"create"}, ResourceNames: []string{"run-a"},
		}})
		var refusal Refusal
		if !errors.As(err, &refusal) || refusal.Code != CodeUnexportedResource {
			t.Fatalf("unexported own-group subresource refusal = %v, want %s", err, CodeUnexportedResource)
		}
	})
	t.Run("cross org target", func(t *testing.T) {
		fixture := newOrgScopeFixture(t, kcppaths.WorkspacePath("org-b", "team-a"), false)
		if _, err := fixture.resolver.Resolve(context.Background(), "org-a", "factory", "team-cluster"); err == nil {
			t.Fatal("cross-org target was accepted")
		}
	})
	t.Run("wrong export source", func(t *testing.T) {
		fixture := newOrgScopeFixture(t, kcppaths.WorkspacePath("org-a", "team-a"), false)
		target := fixture.clients["team-cluster"]
		binding, err := target.Resource(orgIdentityBindingGVR).Get(context.Background(), "factory", metav1.GetOptions{})
		if err != nil {
			t.Fatal(err)
		}
		_ = unstructured.SetNestedField(binding.Object, "root:railgrid:providers:factory", "spec", "reference", "export", "path")
		if _, err := target.Resource(orgIdentityBindingGVR).Update(context.Background(), binding, metav1.UpdateOptions{}); err != nil {
			t.Fatal(err)
		}
		if _, err := fixture.resolver.Resolve(context.Background(), "org-a", "factory", "team-cluster"); err == nil {
			t.Fatal("binding to a different export source was accepted")
		}
	})
}

func TestOrgIdentityForeignRulesNeedCurrentDependencyGrantAndAppliedClaim(t *testing.T) {
	fixture := newOrgScopeFixture(t, kcppaths.WorkspacePath("org-a", "team-a"), true)
	policy, err := fixture.resolver.Resolve(context.Background(), "org-a", "factory", "team-cluster")
	if err != nil {
		t.Fatalf("Resolve with consent: %v", err)
	}
	requested := []rbacv1.PolicyRule{{
		APIGroups: []string{"databricks.example"}, Resources: []string{"tables"}, Verbs: []string{"get"}, ResourceNames: []string{"table-a"},
	}}
	if _, err := policy.Authorize("factory", "team-cluster", requested); err != nil {
		t.Fatalf("consented dependency rule was refused: %v", err)
	}

	fixture.grants.grant = nil
	policy, err = fixture.resolver.Resolve(context.Background(), "org-a", "factory", "team-cluster")
	if err != nil {
		t.Fatalf("Resolve without consent should still allow own-group requests: %v", err)
	}
	if _, err := policy.Authorize("factory", "team-cluster", requested); err == nil {
		t.Fatal("foreign dependency rule was accepted after Grant consent was removed")
	}
}

func TestOrgIdentityDependencyUsesExactBoundPlatformCopyDespiteOrgShadow(t *testing.T) {
	fixture := newOrgScopeFixture(t, kcppaths.WorkspacePath("org-a", "team-a"), true)
	platform, ok := fixture.registry.Get("databricks")
	if !ok {
		t.Fatal("fixture platform dependency is missing")
	}
	platform.Export = &providersv1alpha1.ProviderExport{Name: platform.APIExportName, Resources: []providersv1alpha1.ProviderExportResource{{
		Name: "tables", APIVersion: "databricks.example/v1", Kind: "Table",
		Verbs: []providersv1alpha1.ProviderVerb{{Name: "lookup"}},
	}}}
	fixture.registry.Upsert(platform)
	fixture.registry.Upsert(providers.Provider{
		Name: "databricks", OrgUUID: "org-a",
		APIExportPath: kcppaths.OrgProviderPath("org-a", "databricks"), APIExportName: platform.APIExportName,
		APIGroups: []string{"databricks.example"},
		Export: &providersv1alpha1.ProviderExport{Name: platform.APIExportName, Resources: []providersv1alpha1.ProviderExportResource{{
			Name: "tables", APIVersion: "databricks.example/v1", Kind: "Table",
			Verbs: []providersv1alpha1.ProviderVerb{{Name: "purge"}},
		}}},
	})

	policy, err := fixture.resolver.Resolve(context.Background(), "org-a", "factory", "team-cluster")
	if err != nil {
		t.Fatalf("Resolve with platform dependency binding and same-named org copy: %v", err)
	}
	requested := []rbacv1.PolicyRule{{
		APIGroups: []string{"databricks.example"}, Resources: []string{"tables"}, Verbs: []string{"get"}, ResourceNames: []string{"table-a"},
	}}
	if _, err := policy.Authorize("factory", "team-cluster", requested); err != nil {
		t.Fatalf("platform-bound dependency read was refused due to org shadow: %v", err)
	}
	catalog, ok := policy.catalog.(orgIdentityCatalog)
	if !ok {
		t.Fatalf("policy catalog type = %T", policy.catalog)
	}
	if verbs := catalog.DeclaredVerbs("databricks", "tables"); len(verbs) != 1 || verbs[0] != "lookup" {
		t.Fatalf("bound platform verbs = %v, want [lookup] rather than the org copy's [purge]", verbs)
	}

	platform.WorkspaceCluster = "new-platform-databricks-cluster"
	fixture.registry.Upsert(platform)
	policy, err = fixture.resolver.Resolve(context.Background(), "org-a", "factory", "team-cluster")
	if err != nil {
		t.Fatalf("Resolve with recreated platform source: %v", err)
	}
	if _, err := policy.Authorize("factory", "team-cluster", requested); err == nil {
		t.Fatal("accepted the prior APIBinding issuer after the provider source was recreated")
	}
}

func TestOrgIdentityOwnRuleRefusesAForeignGroup(t *testing.T) {
	registry := providers.NewRegistry()
	registry.Upsert(providers.Provider{Name: "edges", APIGroups: []string{"edges.railgrid.ai"}})
	requester := providers.Provider{Name: "quickstart", APIGroups: []string{"edges.railgrid.ai"}}
	catalog := orgIdentityCatalog{
		registry: registry, orgUUID: "org-a", requester: requester,
		ownResources: map[string]map[string]bool{"edges.railgrid.ai": {"kubernetesclusters": true}},
	}
	requested := []rbacv1.PolicyRule{{
		APIGroups: []string{"edges.railgrid.ai"}, Resources: []string{"kubernetesclusters"}, Verbs: []string{"create", "get", "proxy"}, ResourceNames: []string{"edge-1"},
	}}
	_, err := NewPolicy(catalog, nil).Authorize("quickstart", "cluster-1", requested)
	var refusal Refusal
	if !errors.As(err, &refusal) || refusal.Code != CodeUnexportedResource {
		t.Fatalf("foreign own-group rule refusal = %v, want %s", err, CodeUnexportedResource)
	}

	catalog.requester = providers.Provider{Name: "edges", APIGroups: []string{"edges.railgrid.ai"}}
	if _, err := NewPolicy(catalog, nil).Authorize("edges", "cluster-1", requested); err != nil {
		t.Fatalf("same-name self-hosted provider lost its own group: %v", err)
	}
}

func TestAcceptedAppliedClaimsRejectSelectorsAndUnappliedClaims(t *testing.T) {
	base := map[string]any{"group": "dep.example", "resource": "tables", "verbs": []any{"get"}}
	binding := testUnstructured("apis.kcp.io/v1alpha2", "APIBinding", "factory", nil,
		map[string]any{"permissionClaims": []any{map[string]any{"group": "dep.example", "resource": "tables", "verbs": []any{"get"}, "state": "Accepted"}}},
		map[string]any{
			"appliedPermissionClaims": []any{base},
			"exportPermissionClaims":  []any{base},
		})
	if got := acceptedAppliedClaims(binding)["dep.example/tables"]; !got["get"] {
		t.Fatalf("effective claim = %v, want get", got)
	}
	_ = unstructured.SetNestedSlice(binding.Object, []any{}, "status", "appliedPermissionClaims")
	if got := acceptedAppliedClaims(binding); len(got) != 0 {
		t.Fatalf("unapplied claim was accepted: %v", got)
	}
	_ = unstructured.SetNestedSlice(binding.Object, []any{map[string]any{
		"group": "dep.example", "resource": "tables", "verbs": []any{"get"}, "selector": map[string]any{"matchLabels": map[string]any{"tenant": "a"}},
	}}, "status", "appliedPermissionClaims")
	_ = unstructured.SetNestedSlice(binding.Object, []any{map[string]any{
		"group": "dep.example", "resource": "tables", "verbs": []any{"get"}, "selector": map[string]any{"matchLabels": map[string]any{"tenant": "a"}},
	}}, "status", "exportPermissionClaims")
	if got := acceptedAppliedClaims(binding); len(got) != 0 {
		t.Fatalf("selector-limited claim was escalated into an identity: %v", got)
	}
}

func TestAcceptedAppliedClaimsHandlesKCPMatchAllWithoutWideningSelectors(t *testing.T) {
	cases := []struct {
		name     string
		selector any
		allowed  bool
	}{
		{"KCP unrestricted default", map[string]any{"matchAll": true}, true},
		{"omitted", nil, true},
		{"false", map[string]any{"matchAll": false}, false},
		{"empty", map[string]any{}, false},
		{"malformed", "matchAll", false},
		{"nonboolean", map[string]any{"matchAll": "true"}, false},
		{"labels", map[string]any{"matchLabels": map[string]any{"tenant": "a"}}, false},
		{"mixed", map[string]any{"matchAll": true, "resourceSelector": []any{map[string]any{"name": "one"}}}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			claim := map[string]any{"group": "dep.example", "resource": "tables", "verbs": []any{"get"}}
			accepted := runtime.DeepCopyJSONValue(claim).(map[string]any)
			accepted["state"] = "Accepted"
			applied := runtime.DeepCopyJSONValue(claim).(map[string]any)
			accepted["selector"] = tc.selector
			applied["selector"] = tc.selector
			binding := testUnstructured("apis.kcp.io/v1alpha2", "APIBinding", "factory", nil,
				map[string]any{"permissionClaims": []any{accepted}},
				map[string]any{"appliedPermissionClaims": []any{applied}, "exportPermissionClaims": []any{claim}})
			got := acceptedAppliedClaims(binding)["dep.example/tables"]["get"]
			if got != tc.allowed {
				t.Fatalf("effective get=%v, want %v", got, tc.allowed)
			}
		})
	}
}

func TestOrgIdentityPolicyAdmitsKCPDefaultedUnrestrictedComposition(t *testing.T) {
	fixture := newOrgScopeFixture(t, kcppaths.WorkspacePath("org-a", "team-a"), true)
	target := fixture.clients["team-cluster"]
	binding, err := target.Resource(orgIdentityBindingGVR).Get(context.Background(), "factory", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range [][]string{{"spec", "permissionClaims"}, {"status", "appliedPermissionClaims"}} {
		claims, _, err := unstructured.NestedSlice(binding.Object, path...)
		if err != nil {
			t.Fatal(err)
		}
		for _, raw := range claims {
			raw.(map[string]any)["selector"] = map[string]any{"matchAll": true}
		}
		if err := unstructured.SetNestedSlice(binding.Object, claims, path...); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := target.Resource(orgIdentityBindingGVR).Update(context.Background(), binding, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	policy, err := fixture.resolver.Resolve(context.Background(), "org-a", "factory", "team-cluster")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := policy.Authorize("factory", "team-cluster", []rbacv1.PolicyRule{{APIGroups: []string{"databricks.example"}, Resources: []string{"tables"}, Verbs: []string{"get"}, ResourceNames: []string{"table-a"}}}); err != nil {
		t.Fatalf("unrestricted accepted KCP composition refused: %v", err)
	}
}
