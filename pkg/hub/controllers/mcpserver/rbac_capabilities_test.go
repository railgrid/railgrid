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
	"slices"
	"testing"

	apisv1alpha2 "github.com/kcp-dev/sdk/apis/apis/v1alpha2"
	rbacv1 "k8s.io/api/rbac/v1"

	providersv1alpha1 "github.com/railgrid/railgrid/apis/providers/v1alpha1"
	hubproviders "github.com/railgrid/railgrid/pkg/hub/providers"
)

func TestCuratedDataPlaneGrantsStayWithTheirOwningExport(t *testing.T) {
	tests := []struct {
		name         string
		providerName string
		group        string
		resource     string
		verb         string
		exportPath   string
		exportName   string
		readOnlyVerb string
	}{
		{
			name:         "edges",
			providerName: "edges",
			group:        "edges.railgrid.ai",
			resource:     "linuxservers",
			verb:         "ssh",
			exportPath:   "root:railgrid:providers:edges",
			exportName:   "edges.providers.railgrid.ai",
			readOnlyVerb: "inspect",
		},
		{
			name:         "agents",
			providerName: "agents",
			group:        "agents.railgrid.ai",
			resource:     "modelcredentials",
			verb:         "test",
			exportPath:   "root:railgrid:providers:agents",
			exportName:   "agents.railgrid.ai",
			readOnlyVerb: "inspect",
		},
		{
			name:         "infrastructure",
			providerName: "infrastructure",
			group:        "infrastructure.railgrid.ai",
			resource:     "instances",
			verb:         "exec",
			exportPath:   "root:railgrid:providers:infrastructure",
			exportName:   "infrastructure.providers.railgrid.ai",
			readOnlyVerb: "inspect",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			reg := hubproviders.NewRegistry()
			owner := curatedTestProvider(tc.providerName, tc.exportPath, tc.exportName, tc.group, tc.resource,
				providersv1alpha1.ProviderVerb{Name: tc.verb},
				providersv1alpha1.ProviderVerb{Name: tc.readOnlyVerb, ReadOnly: true},
			)
			foreign := curatedTestProvider("lookalike-"+tc.providerName,
				"root:railgrid:providers:lookalike-"+tc.providerName,
				"lookalike-"+tc.providerName+".providers.railgrid.ai",
				tc.group, tc.resource,
				providersv1alpha1.ProviderVerb{Name: tc.verb},
				providersv1alpha1.ProviderVerb{Name: tc.readOnlyVerb, ReadOnly: true},
			)
			reg.Upsert(owner)
			reg.Upsert(foreign)

			const tenantPath = "root:railgrid:tenants:org-a:workspace-a"
			grants := registrySubresourceGrants(reg, tenantPath)
			ownerGrant, ok := findSubresourceGrant(grants, tc.group, tc.resource, tc.verb, tc.exportPath, tc.exportName)
			if !ok || !ownerGrant.AllowPrivilegedWrite {
				t.Fatalf("owning provider lost curated grant: got %+v, found=%v", ownerGrant, ok)
			}
			if _, ok := findSubresourceGrant(grants, tc.group, tc.resource, tc.verb, foreign.APIExportPath, foreign.APIExportName); ok {
				t.Fatalf("foreign export inherited curated grant: %+v", grants)
			}
			if grant, ok := findSubresourceGrant(grants, tc.group, tc.resource, tc.readOnlyVerb, foreign.APIExportPath, foreign.APIExportName); !ok || !grant.ReadOnly || grant.AllowPrivilegedWrite {
				t.Fatalf("foreign export's generic read-only declaration was not kept separate: %+v, found=%v", grant, ok)
			}

			ownerBound := []BoundResource{curatedBoundResource(tc.group, tc.resource, tc.exportPath, tc.exportName)}
			ownerRules := buildRules(ownerBound, grants, false)
			if !hasSubresourceRule(ownerRules, tc.group, tc.resource+"/"+tc.verb) {
				t.Fatalf("writable MCPServer lost owner's curated %s/%s grant: %+v", tc.resource, tc.verb, ownerRules)
			}
			if hasSubresourceRule(buildRules(ownerBound, grants, true), tc.group, tc.resource+"/"+tc.verb) {
				t.Fatalf("read-only MCPServer retained non-read-only curated %s/%s grant", tc.resource, tc.verb)
			}

			foreignBound := []BoundResource{curatedBoundResource(tc.group, tc.resource, foreign.APIExportPath, foreign.APIExportName)}
			foreignRules := buildRules(foreignBound, grants, false)
			if hasSubresourceRule(foreignRules, tc.group, tc.resource+"/"+tc.verb) {
				t.Fatalf("foreign export received the curated %s/%s exception: %+v", tc.resource, tc.verb, foreignRules)
			}
			if !hasSubresourceRule(foreignRules, tc.group, tc.resource+"/"+tc.readOnlyVerb) ||
				!hasSubresourceRule(buildRules(foreignBound, grants, true), tc.group, tc.resource+"/"+tc.readOnlyVerb) {
				t.Fatalf("foreign export's explicitly read-only verb did not follow generic policy")
			}
		})
	}
}

func TestCuratedInvocationRoutesStayWritableOnlyWhenMisdeclaredReadOnly(t *testing.T) {
	tests := []struct {
		providerName string
		group        string
		resource     string
		verb         string
		exportPath   string
		exportName   string
	}{
		{"edges", "edges.railgrid.ai", "kubernetesclusters", "k8s", "root:railgrid:providers:edges", "edges.providers.railgrid.ai"},
		{"edges", "edges.railgrid.ai", "kubernetesclusters", "ssh", "root:railgrid:providers:edges", "edges.providers.railgrid.ai"},
		{"edges", "edges.railgrid.ai", "kubernetesclusters", "mcp", "root:railgrid:providers:edges", "edges.providers.railgrid.ai"},
		{"edges", "edges.railgrid.ai", "linuxservers", "k8s", "root:railgrid:providers:edges", "edges.providers.railgrid.ai"},
		{"edges", "edges.railgrid.ai", "linuxservers", "ssh", "root:railgrid:providers:edges", "edges.providers.railgrid.ai"},
		{"edges", "edges.railgrid.ai", "services", "proxy", "root:railgrid:providers:edges", "edges.providers.railgrid.ai"},
		{"edges", "edges.railgrid.ai", "services", "mcp", "root:railgrid:providers:edges", "edges.providers.railgrid.ai"},
		{"infrastructure", "infrastructure.railgrid.ai", "instances", "exec", "root:railgrid:providers:infrastructure", "infrastructure.providers.railgrid.ai"},
	}

	for _, tc := range tests {
		t.Run(tc.group+"/"+tc.resource+"/"+tc.verb, func(t *testing.T) {
			reg := hubproviders.NewRegistry()
			reg.Upsert(curatedTestProvider(tc.providerName, tc.exportPath, tc.exportName, tc.group, tc.resource,
				providersv1alpha1.ProviderVerb{Name: tc.verb, ReadOnly: true},
			))
			const tenantPath = "root:railgrid:tenants:org-a:workspace-a"
			grants := registrySubresourceGrants(reg, tenantPath)
			grant, ok := findSubresourceGrant(grants, tc.group, tc.resource, tc.verb, tc.exportPath, tc.exportName)
			if !ok || grant.ReadOnly || !grant.AllowPrivilegedWrite {
				t.Fatalf("curated route did not retain writable-only provenance: %+v, found=%v", grant, ok)
			}

			bound := []BoundResource{curatedBoundResource(tc.group, tc.resource, tc.exportPath, tc.exportName)}
			if !hasSubresourceRule(buildRules(bound, grants, false), tc.group, tc.resource+"/"+tc.verb) {
				t.Fatalf("writable MCPServer lost curated route %s/%s after a ReadOnly mislabel", tc.resource, tc.verb)
			}
			if hasSubresourceRule(buildRules(bound, grants, true), tc.group, tc.resource+"/"+tc.verb) {
				t.Fatalf("read-only MCPServer received curated route %s/%s after a ReadOnly mislabel", tc.resource, tc.verb)
			}

			// The deny also applies to directly supplied action or verb grants;
			// the registry source is not the only enforcement boundary.
			injectedReadOnly := SubresourceGrant{
				Group: tc.group, Resource: tc.resource, Name: tc.verb, ReadOnly: true,
				APIExportPath: tc.exportPath, APIExportName: tc.exportName,
			}
			for _, readOnly := range []bool{false, true} {
				if hasSubresourceRule(buildRules(bound, []SubresourceGrant{injectedReadOnly}, readOnly), tc.group, tc.resource+"/"+tc.verb) {
					t.Fatalf("readOnly=%v accepted directly supplied dangerous ReadOnly coordinate %s/%s", readOnly, tc.resource, tc.verb)
				}
			}
		})
	}
}

func curatedTestProvider(name, exportPath, exportName, group, resource string, verbs ...providersv1alpha1.ProviderVerb) hubproviders.Provider {
	return hubproviders.Provider{
		Name:          name,
		APIExportPath: exportPath,
		APIExportName: exportName,
		APIGroups:     []string{group},
		Export: &providersv1alpha1.ProviderExport{
			Name: exportName,
			Resources: []providersv1alpha1.ProviderExportResource{{
				Name: resource, APIVersion: group + "/v1alpha1", Kind: "Example", Verbs: verbs,
			}},
		},
	}
}

func curatedBoundResource(group, resource, exportPath, exportName string) BoundResource {
	return BoundResource{
		BoundAPIResource: apisv1alpha2.BoundAPIResource{Group: group, Resource: resource},
		APIExportPath:    exportPath,
		APIExportName:    exportName,
	}
}

func findSubresourceGrant(grants []SubresourceGrant, group, resource, name, exportPath, exportName string) (SubresourceGrant, bool) {
	for _, grant := range grants {
		if grant.Group == group && grant.Resource == resource && grant.Name == name &&
			grant.APIExportPath == exportPath && grant.APIExportName == exportName {
			return grant, true
		}
	}
	return SubresourceGrant{}, false
}

func hasSubresourceRule(rules []rbacv1.PolicyRule, group, resource string) bool {
	for _, rule := range rules {
		if slices.Contains(rule.APIGroups, group) && slices.Contains(rule.Resources, resource) {
			return true
		}
	}
	return false
}
