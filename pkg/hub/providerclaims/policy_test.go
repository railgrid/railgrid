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

package providerclaims

import (
	"reflect"
	"testing"

	providersv1alpha1 "github.com/railgrid/railgrid/apis/providers/v1alpha1"
	tenancyv1alpha1 "github.com/railgrid/railgrid/apis/tenancy/v1alpha1"
	"github.com/railgrid/railgrid/pkg/hub/hubaccess"
	"github.com/railgrid/railgrid/pkg/hub/kcp"
	"github.com/railgrid/railgrid/pkg/hub/providers"
	"github.com/railgrid/railgrid/pkg/kcppaths"
)

// agentsProvider is a provider with every kind of requirement: a scoped core
// claim, two platform builtins, a composition on code, and a verb coordinate.
func agentsProvider() providers.Provider {
	return providers.Provider{
		Name:                "agents",
		APIExportPath:       kcppaths.ProviderPath("agents"),
		APIExportName:       "agents.railgrid.ai",
		CatalogEntryCluster: "providers-agents-cluster",
		Requires: []providersv1alpha1.ProviderRequirement{{
			Resources: []providersv1alpha1.ProviderRequiredResource{{
				Name:     "secrets",
				Verbs:    []providersv1alpha1.ProviderRequiredVerb{"get", "list", "watch"},
				Selector: &providersv1alpha1.ProviderLabelSelector{MatchLabels: map[string]string{"railgrid.ai/owner": "agents"}},
			}},
		}, {
			Group: "authentication.k8s.io",
			Resources: []providersv1alpha1.ProviderRequiredResource{{
				Name: "tokenreviews", Verbs: []providersv1alpha1.ProviderRequiredVerb{"create"},
			}},
		}, {
			Provider: "code",
			Group:    "code.railgrid.ai",
			Resources: []providersv1alpha1.ProviderRequiredResource{
				{Name: "repositories", Verbs: []providersv1alpha1.ProviderRequiredVerb{"get"}},
				{Name: "repositories/sync"},
			},
		}},
	}
}

func grantWith(capabilities ...string) *tenancyv1alpha1.Grant {
	g := &tenancyv1alpha1.Grant{}
	for _, c := range capabilities {
		g.Spec.Capabilities = append(g.Spec.Capabilities, tenancyv1alpha1.GrantedCapability{Capability: c, Scope: "workspace"})
	}
	return g
}

func TestDesired_AcceptsOwnRequirementsAndOnlyConsentedCompositions(t *testing.T) {
	prov := agentsProvider()

	t.Run("no grant: compositions are declared but Rejected", func(t *testing.T) {
		got := Desired(prov, nil)
		want := []kcp.ProviderClaim{
			{Resource: "secrets", Verbs: []string{"get", "list", "watch"}, Accepted: true, MatchLabels: map[string]string{"railgrid.ai/owner": "agents"}},
			{Group: "authentication.k8s.io", Resource: "tokenreviews", Verbs: []string{"create"}, Accepted: true},
			{Group: "code.railgrid.ai", Resource: "repositories", Verbs: []string{"get"}, Accepted: false},
			{Group: "code.railgrid.ai", Resource: "repositories/sync", Verbs: []string{"*"}, Accepted: false},
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("Desired(nil grant) =\n%+v\nwant\n%+v", got, want)
		}
	})

	t.Run("grant consents to one composition", func(t *testing.T) {
		got := Desired(prov, grantWith(hubaccess.ComposeCapability("code.railgrid.ai", "repositories")))
		byKey := map[string]bool{}
		for _, c := range got {
			byKey[Key(c.Group, c.Resource)] = c.Accepted
		}
		if !byKey["code.railgrid.ai/repositories"] {
			t.Fatalf("consented composition not accepted: %+v", got)
		}
		for _, c := range got {
			wantConsented := c.Group == "code.railgrid.ai" && c.Resource == "repositories"
			if c.Consented != wantConsented {
				t.Fatalf("claim %s/%s Consented=%v, want %v (only a grant-backed composition is Consented)", c.Group, c.Resource, c.Consented, wantConsented)
			}
		}
		if byKey["code.railgrid.ai/repositories/sync"] {
			t.Fatalf("unconsented verb coordinate accepted: %+v", got)
		}
		if !byKey["/secrets"] || !byKey["authentication.k8s.io/tokenreviews"] {
			t.Fatalf("own requirements must stay accepted: %+v", got)
		}
	})

	t.Run("unrelated capabilities grant nothing", func(t *testing.T) {
		got := Desired(prov, grantWith("memberships.read"))
		for _, c := range got {
			if c.Group == "code.railgrid.ai" && c.Accepted {
				t.Fatalf("composition accepted from a non-compose capability: %+v", c)
			}
		}
	})
}

func TestDesired_NoRequirementsIsEmpty(t *testing.T) {
	if got := Desired(providers.Provider{Name: "quiet"}, nil); len(got) != 0 {
		t.Fatalf("Desired() = %+v, want empty", got)
	}
}

func TestDesiredOrgProviderRequiresNewEnableConsent(t *testing.T) {
	prov := agentsProvider()
	prov.OrgUUID = "org-a"
	prov.APIExportPath = kcppaths.OrgProviderPath("org-a", prov.Name)
	for _, grant := range []*tenancyv1alpha1.Grant{nil, grantWith(hubaccess.ComposeCapability("code.railgrid.ai", "repositories"))} {
		claims := Desired(prov, grant)
		for _, claim := range claims {
			if claim.Accepted || claim.Consented {
				t.Errorf("background reconciliation grants new org claim: %+v", claim)
			}
			own := claim.Group == "" || claim.Group == "authentication.k8s.io"
			compositionConsent := grant != nil && claim.Group == "code.railgrid.ai" && claim.Resource == "repositories"
			if claim.PreserveExistingAcceptance != (own || compositionConsent) {
				t.Errorf("wrong preservation policy: %+v", claim)
			}
		}
	}
}
