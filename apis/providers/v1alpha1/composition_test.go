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

package v1alpha1

import (
	"strings"
	"testing"
)

func TestValidateProviderCompositionsAcceptsAppStudiosDeclaration(t *testing.T) {
	if err := ValidateProviderCompositions([]ProviderDependency{
		{Name: "infrastructure", Composes: []ProviderComposition{{
			Group: "infrastructure.railgrid.ai", Resource: "instances",
			Verbs: []ProviderCompositionVerb{"get", "list", "watch", "create", "update", "delete"},
		}}},
		{Name: "code", Composes: []ProviderComposition{
			{Group: "code.railgrid.ai", Resource: "repositories",
				Verbs: []ProviderCompositionVerb{"get", "list", "watch", "create", "update"}},
			{Group: "code.railgrid.ai", Resource: "repositorycommits",
				Verbs: []ProviderCompositionVerb{"get", "list", "watch"}},
		}},
	}); err != nil {
		t.Fatalf("App Studio's declaration was rejected: %v", err)
	}
}

func TestValidateProviderCompositionsRejectsMalformedDeclarations(t *testing.T) {
	for _, tc := range []struct {
		name        string
		composition ProviderComposition
		want        string
	}{
		{name: "no group", composition: ProviderComposition{Resource: "instances", Verbs: []ProviderCompositionVerb{"get"}}, want: "group must be"},
		{name: "wildcard group", composition: ProviderComposition{Group: "*", Resource: "instances", Verbs: []ProviderCompositionVerb{"get"}}, want: "group must be"},
		{name: "uppercase group", composition: ProviderComposition{Group: "Infrastructure.railgrid.ai", Resource: "instances", Verbs: []ProviderCompositionVerb{"get"}}, want: "group must be"},
		{name: "no resource", composition: ProviderComposition{Group: "infrastructure.railgrid.ai", Verbs: []ProviderCompositionVerb{"get"}}, want: "resource must be"},
		{name: "wildcard resource", composition: ProviderComposition{Group: "infrastructure.railgrid.ai", Resource: "*", Verbs: []ProviderCompositionVerb{"get"}}, want: "resource must be"},
		{name: "subresource", composition: ProviderComposition{Group: "infrastructure.railgrid.ai", Resource: "instances/status", Verbs: []ProviderCompositionVerb{"get"}}, want: "resource must be"},
		{name: "no verbs", composition: ProviderComposition{Group: "infrastructure.railgrid.ai", Resource: "instances"}, want: "at least one verb"},
		{name: "wildcard verb", composition: ProviderComposition{Group: "infrastructure.railgrid.ai", Resource: "instances", Verbs: []ProviderCompositionVerb{"*"}}, want: "is not one of"},
		{name: "a data-plane verb is not a composition verb", composition: ProviderComposition{Group: "infrastructure.railgrid.ai", Resource: "instances", Verbs: []ProviderCompositionVerb{"exec"}}, want: "is not one of"},
		{name: "deletecollection", composition: ProviderComposition{Group: "infrastructure.railgrid.ai", Resource: "instances", Verbs: []ProviderCompositionVerb{"deletecollection"}}, want: "is not one of"},
		{name: "duplicate verb", composition: ProviderComposition{Group: "infrastructure.railgrid.ai", Resource: "instances", Verbs: []ProviderCompositionVerb{"get", "get"}}, want: "duplicate verb"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateProviderCompositions([]ProviderDependency{{Name: "infrastructure", Composes: []ProviderComposition{tc.composition}}})
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v, want one containing %q", err, tc.want)
			}
		})
	}
}

func TestValidateProviderCompositionsRejectsDuplicateKinds(t *testing.T) {
	err := ValidateProviderCompositions([]ProviderDependency{{Name: "infrastructure", Composes: []ProviderComposition{
		{Group: "infrastructure.railgrid.ai", Resource: "instances", Verbs: []ProviderCompositionVerb{"get"}},
		{Group: "infrastructure.railgrid.ai", Resource: "instances", Verbs: []ProviderCompositionVerb{"delete"}},
	}}})
	if err == nil || !strings.Contains(err.Error(), "duplicate composed resource") {
		t.Fatalf("error = %v, want a duplicate-resource refusal", err)
	}
}

func TestValidateProviderCompositionsAllowsNone(t *testing.T) {
	if err := ValidateProviderCompositions([]ProviderDependency{{Name: "infrastructure"}}); err != nil {
		t.Fatalf("a dependency with no compositions was rejected: %v", err)
	}
}
