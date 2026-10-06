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

package mcpserver

import (
	"context"
	"slices"
	"testing"

	apisv1alpha2 "github.com/kcp-dev/sdk/apis/apis/v1alpha2"
	kcpfake "github.com/kcp-dev/sdk/client/clientset/versioned/fake"
	rbacv1 "k8s.io/api/rbac/v1"

	providersv1alpha1 "github.com/railgrid/railgrid/apis/providers/v1alpha1"
	hubproviders "github.com/railgrid/railgrid/pkg/hub/providers"
)

func grantTestProvider(name, orgUUID, group, exportPath string, verbs []providersv1alpha1.ProviderVerb, actions []providersv1alpha1.ProviderAction) hubproviders.Provider {
	exportName := name + ".providers.railgrid.ai"
	return hubproviders.Provider{
		Name:          name,
		OrgUUID:       orgUUID,
		APIExportPath: exportPath,
		APIExportName: exportName,
		APIGroups:     []string{group},
		Export: &providersv1alpha1.ProviderExport{
			Name: exportName,
			Resources: []providersv1alpha1.ProviderExportResource{{
				Name:       "widgets",
				APIVersion: group + "/v1alpha1",
				Kind:       "Widget",
				Verbs:      verbs,
				Actions:    actions,
			}},
		},
	}
}

func grantCoordinateSet(grants []SubresourceGrant) map[string]SubresourceGrant {
	got := make(map[string]SubresourceGrant, len(grants))
	for _, grant := range grants {
		got[grant.Group+"/"+grant.Resource+"/"+grant.Name] = grant
	}
	return got
}

func TestRegistrySubresourceGrants_UsesProviderOwnedDeclarationsAndTenantVisibility(t *testing.T) {
	reg := hubproviders.NewRegistry()
	reg.Upsert(grantTestProvider("demo", "", "demo.railgrid.ai", "root:railgrid:providers:demo",
		[]providersv1alpha1.ProviderVerb{
			{Name: "inspect", ReadOnly: true},
			{Name: "apply", ReadOnly: false},
		},
		[]providersv1alpha1.ProviderAction{
			{Name: "lookup", ReadOnly: true},
			{Name: "rebuild", ReadOnly: false},
		},
	))
	reg.Upsert(grantTestProvider("shadow", "", "platform-shadow.railgrid.ai", "root:railgrid:providers:shadow",
		[]providersv1alpha1.ProviderVerb{{Name: "describe", ReadOnly: true}}, nil))
	reg.Upsert(grantTestProvider("shadow", "org-a", "org-shadow.railgrid.ai", "root:railgrid:tenants:org-a:providers:shadow",
		[]providersv1alpha1.ProviderVerb{{Name: "private_read", ReadOnly: true}}, nil))
	reg.Upsert(grantTestProvider("local", "org-a", "local.railgrid.ai", "root:railgrid:tenants:org-a:providers:local",
		[]providersv1alpha1.ProviderVerb{{Name: "local_read", ReadOnly: true}}, nil))
	reg.Upsert(grantTestProvider("foreign", "org-b", "foreign.railgrid.ai", "root:railgrid:tenants:org-b:providers:foreign",
		[]providersv1alpha1.ProviderVerb{{Name: "foreign_read", ReadOnly: true}}, nil))

	grants := registrySubresourceGrants(reg, "root:railgrid:tenants:org-a:workspace-a")
	byCoordinate := grantCoordinateSet(grants)
	for _, coordinate := range []string{
		"demo.railgrid.ai/widgets/inspect",
		"demo.railgrid.ai/widgets/lookup",
		"demo.railgrid.ai/widgets/rebuild",
	} {
		if _, ok := byCoordinate[coordinate]; !ok {
			t.Errorf("visible platform declaration %q missing from grants: %+v", coordinate, grants)
		}
	}
	for _, coordinate := range []string{
		"demo.railgrid.ai/widgets/apply",               // mutating custom verbs are not generically granted
		"platform-shadow.railgrid.ai/widgets/describe", // shadowed before org-owned providers are filtered
		"org-shadow.railgrid.ai/widgets/private_read",
		"local.railgrid.ai/widgets/local_read",
		"foreign.railgrid.ai/widgets/foreign_read",
	} {
		if _, ok := byCoordinate[coordinate]; ok {
			t.Errorf("ineligible declaration %q was granted: %+v", coordinate, grants)
		}
	}
	for _, coordinate := range []string{"demo.railgrid.ai/widgets/inspect", "demo.railgrid.ai/widgets/lookup"} {
		if !byCoordinate[coordinate].ReadOnly {
			t.Errorf("read-only declaration %q lost its readOnly marker: %+v", coordinate, byCoordinate[coordinate])
		}
	}
	if byCoordinate["demo.railgrid.ai/widgets/rebuild"].ReadOnly {
		t.Fatal("mutable action was incorrectly marked read-only")
	}
	if got := byCoordinate["demo.railgrid.ai/widgets/inspect"]; got.APIExportPath != "root:railgrid:providers:demo" || got.APIExportName != "demo.providers.railgrid.ai" {
		t.Fatalf("grant lost provider export identity: %+v", got)
	}

	// A ServiceAccount with no tenant path sees only platform providers. An
	// unknown or non-tenant path must not fall back to the global provider set.
	if got := registrySubresourceGrants(reg, "root:railgrid:providers:demo"); len(got) != 0 {
		t.Fatalf("provider workspace path unexpectedly produced grants: %+v", got)
	}
	if got := registrySubresourceGrants(reg, ""); len(got) != 0 {
		t.Fatalf("missing tenant path unexpectedly produced grants: %+v", got)
	}
	if got := registrySubresourceGrants(nil, "root:railgrid:tenants:org-a:workspace-a"); len(got) != 0 {
		t.Fatalf("nil registry unexpectedly produced grants: %+v", got)
	}
}

func TestRegistrySubresourceGrants_MalformedCoordinatesFailClosedDeterministically(t *testing.T) {
	reg := hubproviders.NewRegistry()
	provider := grantTestProvider("demo", "", "demo.railgrid.ai", "root:railgrid:providers:demo",
		[]providersv1alpha1.ProviderVerb{
			{Name: "inspect", ReadOnly: true},
			{Name: "BadVerb", ReadOnly: true},
			{Name: "unsafe/coordinate", ReadOnly: true},
		},
		[]providersv1alpha1.ProviderAction{
			{Name: "lookup", ReadOnly: true},
			{Name: "BadAction", ReadOnly: true},
			{Name: "unsafe/action", ReadOnly: true},
		},
	)
	provider.Export.Resources = append(provider.Export.Resources,
		providersv1alpha1.ProviderExportResource{
			Name: "jobs", APIVersion: "v1", Kind: "Job",
			Verbs: []providersv1alpha1.ProviderVerb{{Name: "unknown_group", ReadOnly: true}},
		},
		providersv1alpha1.ProviderExportResource{
			Name: "foreign", APIVersion: "other.railgrid.ai/v1alpha1", Kind: "Foreign",
			Verbs: []providersv1alpha1.ProviderVerb{{Name: "wrong_group", ReadOnly: true}},
		},
	)
	reg.Upsert(provider)

	path := "root:railgrid:tenants:org-a:workspace-a"
	first := registrySubresourceGrants(reg, path)
	second := registrySubresourceGrants(reg, path)
	if !slices.Equal(first, second) {
		t.Fatalf("grant parsing is nondeterministic:\nfirst:  %+v\nsecond: %+v", first, second)
	}
	byCoordinate := grantCoordinateSet(first)
	if len(byCoordinate) != 2 {
		t.Fatalf("malformed declarations were not rejected while valid siblings remained: %+v", first)
	}
	for _, coordinate := range []string{"demo.railgrid.ai/widgets/inspect", "demo.railgrid.ai/widgets/lookup"} {
		if _, ok := byCoordinate[coordinate]; !ok {
			t.Errorf("valid sibling %q was dropped: %+v", coordinate, first)
		}
	}
	for _, coordinate := range []string{
		"demo.railgrid.ai/widgets/BadVerb",
		"demo.railgrid.ai/widgets/unsafe/coordinate",
		"demo.railgrid.ai/widgets/BadAction",
		"demo.railgrid.ai/widgets/unsafe/action",
		"/jobs/unknown_group",
		"other.railgrid.ai/foreign/wrong_group",
	} {
		if _, ok := byCoordinate[coordinate]; ok {
			t.Errorf("malformed or unowned declaration %q was granted: %+v", coordinate, first)
		}
	}
}

func TestRegistrySubresourceGrants_DenyEdgeCredentialCoordinatesForEitherServerMode(t *testing.T) {
	reg := hubproviders.NewRegistry()
	edge := grantTestProvider("edges", "", "edges.railgrid.ai", "root:railgrid:providers:edges", nil, nil)
	edge.Export.Resources = []providersv1alpha1.ProviderExportResource{
		{
			Name: "kubernetesclusters", APIVersion: "edges.railgrid.ai/v1alpha1", Kind: "KubernetesCluster",
			Verbs:   []providersv1alpha1.ProviderVerb{{Name: "k8s"}, {Name: "runner-auth", ReadOnly: true}, {Name: "runner-token", ReadOnly: true}},
			Actions: []providersv1alpha1.ProviderAction{{Name: "agent-token", ReadOnly: true}},
		},
		{
			Name: "linuxservers", APIVersion: "edges.railgrid.ai/v1alpha1", Kind: "LinuxServer",
			Verbs: []providersv1alpha1.ProviderVerb{{Name: "ssh-credentials", ReadOnly: true}, {Name: "agent-token", ReadOnly: true}, {Name: "runner-auth", ReadOnly: true}, {Name: "runner-token", ReadOnly: true}},
		},
		{
			Name: "macosservers", APIVersion: "edges.railgrid.ai/v1alpha1", Kind: "MacOSServer",
			Verbs: []providersv1alpha1.ProviderVerb{{Name: "agent-token", ReadOnly: true}, {Name: "runner-auth", ReadOnly: true}, {Name: "runner-token", ReadOnly: true}},
		},
		{
			Name: "services", APIVersion: "edges.railgrid.ai/v1alpha1", Kind: "Service",
			Verbs: []providersv1alpha1.ProviderVerb{{Name: "ticket", ReadOnly: true}},
		},
	}
	reg.Upsert(edge)

	const exportPath = "root:railgrid:providers:edges"
	const exportName = "edges.providers.railgrid.ai"
	boundResources := []BoundResource{
		{BoundAPIResource: apisBound("edges.railgrid.ai", "kubernetesclusters"), APIExportPath: exportPath, APIExportName: exportName},
		{BoundAPIResource: apisBound("edges.railgrid.ai", "linuxservers"), APIExportPath: exportPath, APIExportName: exportName},
		{BoundAPIResource: apisBound("edges.railgrid.ai", "macosservers"), APIExportPath: exportPath, APIExportName: exportName},
		{BoundAPIResource: apisBound("edges.railgrid.ai", "services"), APIExportPath: exportPath, APIExportName: exportName},
	}
	grants := registrySubresourceGrants(reg, "root:railgrid:tenants:org-a:workspace-a")
	denied := []string{
		"kubernetesclusters/agent-token", "linuxservers/agent-token", "macosservers/agent-token",
		"linuxservers/ssh-credentials", "kubernetesclusters/runner-auth", "kubernetesclusters/runner-token",
		"linuxservers/runner-auth", "linuxservers/runner-token", "macosservers/runner-auth", "macosservers/runner-token",
		"services/ticket",
	}
	for _, readOnly := range []bool{false, true} {
		// A directly supplied action grant must hit the same coordinate deny as
		// a registry-projected custom verb.
		injected := append(slices.Clone(grants), SubresourceGrant{
			Group: "edges.railgrid.ai", Resource: "kubernetesclusters", Name: "agent-token", ReadOnly: true,
			APIExportPath: exportPath, APIExportName: exportName,
		})
		rules := buildRules(boundResources, injected, readOnly)
		for _, coordinate := range denied {
			if grant := findRule(t, rules, "edges.railgrid.ai", coordinate); grant != nil {
				t.Errorf("readOnly=%v granted edge credential/browser coordinate %q: %+v", readOnly, coordinate, grant)
			}
		}
	}
}

func apisBound(group, resource string) apisv1alpha2.BoundAPIResource {
	return apisv1alpha2.BoundAPIResource{Group: group, Resource: resource}
}

func TestDesiredRules_UsesProviderRegistryAndExactBoundExportIdentity(t *testing.T) {
	const tenantPath = "root:railgrid:tenants:org-a:workspace-a"
	reg := hubproviders.NewRegistry()
	provider := grantTestProvider("demo", "", "demo.railgrid.ai", "root:railgrid:providers:demo",
		[]providersv1alpha1.ProviderVerb{{Name: "inspect", ReadOnly: true}, {Name: "mutate"}},
		[]providersv1alpha1.ProviderAction{{Name: "query", ReadOnly: true}, {Name: "rebuild"}},
	)
	reg.Upsert(provider)
	kcp := kcpfake.NewSimpleClientset(newBinding("demo", bound("demo.railgrid.ai", "widgets")))
	r := &Reconciler{providerRegistry: reg}
	ctx := context.Background()

	for _, tc := range []struct {
		name        string
		binding     *apisv1alpha2.APIBinding
		wantInspect bool
	}{
		{name: "registry declaration without central CatalogEntry", binding: newBinding("demo", bound("demo.railgrid.ai", "widgets")), wantInspect: true},
		{name: "wrong export path", binding: func() *apisv1alpha2.APIBinding {
			b := newBinding("demo", bound("demo.railgrid.ai", "widgets"))
			b.Spec.Reference.Export.Path = "root:railgrid:providers:other"
			return b
		}()},
		{name: "wrong export name", binding: func() *apisv1alpha2.APIBinding {
			b := newBinding("demo", bound("demo.railgrid.ai", "widgets"))
			b.Spec.Reference.Export.Name = "other.providers.railgrid.ai"
			return b
		}()},
		{name: "resource is not bound", binding: newBinding("demo", bound("demo.railgrid.ai", "gadgets"))},
		{name: "export path omitted and local fallback cannot match provider export", binding: func() *apisv1alpha2.APIBinding {
			b := newBinding("demo", bound("demo.railgrid.ai", "widgets"))
			b.Spec.Reference.Export.Path = ""
			return b
		}()},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fakeKCP := kcpfake.NewSimpleClientset(tc.binding)
			rules, err := r.desiredRules(ctx, fakeKCP, newServer("mcp", false), tenantPath)
			if err != nil {
				t.Fatalf("desiredRules: %v", err)
			}
			got := findRule(t, rules, "demo.railgrid.ai", "widgets/inspect") != nil
			if got != tc.wantInspect {
				t.Fatalf("widgets/inspect present = %v, want %v; rules=%+v", got, tc.wantInspect, rules)
			}
			if tc.wantInspect {
				for _, coordinate := range []string{"widgets/mutate"} {
					if findRule(t, rules, "demo.railgrid.ai", coordinate) != nil {
						t.Errorf("mutable custom verb %q was granted: %+v", coordinate, rules)
					}
				}
			}
		})
	}

	// No dynamic CatalogEntry client or cache is involved: changing the registry
	// declaration changes the next desiredRules result immediately.
	readOnlyRules := func(readOnly bool) []rbacv1.PolicyRule {
		t.Helper()
		rules, err := r.desiredRules(ctx, kcp, newServer("mcp", readOnly), tenantPath)
		if err != nil {
			t.Fatalf("desiredRules(readOnly=%v): %v", readOnly, err)
		}
		return rules
	}
	assertCoordinate := func(rules []rbacv1.PolicyRule, coordinate string, want bool) {
		t.Helper()
		got := findRule(t, rules, "demo.railgrid.ai", coordinate) != nil
		if got != want {
			t.Fatalf("%s present = %v, want %v; rules=%+v", coordinate, got, want, rules)
		}
	}

	assertCoordinate(readOnlyRules(true), "widgets/inspect", true)
	assertCoordinate(readOnlyRules(true), "widgets/query", true)
	assertCoordinate(readOnlyRules(true), "widgets/rebuild", false)
	assertCoordinate(readOnlyRules(false), "widgets/inspect", true)
	assertCoordinate(readOnlyRules(false), "widgets/mutate", false)
	assertCoordinate(readOnlyRules(false), "widgets/rebuild", true)

	updated, ok := reg.Get("demo")
	if !ok {
		t.Fatal("provider missing from registry")
	}
	updated.Export = updated.Export.DeepCopy()
	updated.Export.Resources[0].Verbs[0].ReadOnly = false
	updated.Export.Resources[0].Actions = updated.Export.Resources[0].Actions[:1]
	updated.Export.Resources[0].Actions[0].ReadOnly = false
	reg.Upsert(updated)
	assertCoordinate(readOnlyRules(true), "widgets/inspect", false)
	assertCoordinate(readOnlyRules(true), "widgets/query", false)
	assertCoordinate(readOnlyRules(false), "widgets/inspect", false)
	assertCoordinate(readOnlyRules(false), "widgets/query", true)
	assertCoordinate(readOnlyRules(false), "widgets/rebuild", false)
}
