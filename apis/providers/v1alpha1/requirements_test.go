/*
Copyright 2026 The Railgrid Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package v1alpha1

import (
	"strings"
	"testing"
)

// appStudioRequirements is the real declaration: two providers' kinds, two of
// their verbs, and the platform builtins its own machinery needs.
func appStudioRequirements() []ProviderRequirement {
	return []ProviderRequirement{{
		Provider: "infrastructure",
		Group:    "infrastructure.railgrid.ai",
		Resources: []ProviderRequiredResource{
			{Name: "instances", Verbs: []ProviderRequiredVerb{"get", "list", "watch", "create", "update", "delete"}},
			{Name: "instances/exec"},
			{Name: "instances/log"},
		},
	}, {
		Provider: "code",
		Group:    "code.railgrid.ai",
		Resources: []ProviderRequiredResource{
			{Name: "repositories", Verbs: []ProviderRequiredVerb{"get", "list", "watch", "create", "update"}},
			{Name: "repositorycommits", Verbs: []ProviderRequiredVerb{"get", "list", "watch"}},
			{Name: "connections", Verbs: []ProviderRequiredVerb{"get", "list", "watch"}},
			{Name: "repositories/commit"},
			{Name: "connections/mint-registry-token"},
		},
	}, {
		Group: "authorization.k8s.io",
		Resources: []ProviderRequiredResource{
			{Name: "subjectaccessreviews", Verbs: []ProviderRequiredVerb{"create"}},
		},
	}, {
		Resources: []ProviderRequiredResource{{
			Name:     "secrets",
			Verbs:    []ProviderRequiredVerb{"get", "list", "watch", "create", "update", "delete"},
			Selector: &ProviderLabelSelector{MatchLabels: map[string]string{"railgrid.ai/owner": "app-studio"}},
		}},
	}}
}

func TestValidateProviderRequirementsAcceptsAppStudiosDeclaration(t *testing.T) {
	if err := ValidateProviderRequirements(appStudioRequirements()); err != nil {
		t.Fatalf("App Studio's declaration was rejected: %v", err)
	}
	if err := ValidateProviderRequirements(nil); err != nil {
		t.Fatalf("a provider needing nothing was rejected: %v", err)
	}
}

func TestValidateProviderRequirementsRejectsWhatItMust(t *testing.T) {
	for _, tc := range []struct {
		name        string
		requirement ProviderRequirement
		want        string
	}{{
		name:        "no resources",
		requirement: ProviderRequirement{Provider: "code", Group: "code.railgrid.ai"},
		want:        "at least one resource",
	}, {
		name: "a plain resource needs verbs",
		requirement: ProviderRequirement{Provider: "code", Group: "code.railgrid.ai",
			Resources: []ProviderRequiredResource{{Name: "repositories"}}},
		want: "at least one verb",
	}, {
		name: "a verb coordinate carries no verbs",
		requirement: ProviderRequirement{Provider: "code", Group: "code.railgrid.ai",
			Resources: []ProviderRequiredResource{{Name: "repositories/commit", Verbs: []ProviderRequiredVerb{"create"}}}},
		want: "verbs must be empty",
	}, {
		name: "a verb coordinate carries no selector",
		requirement: ProviderRequirement{Provider: "code", Group: "code.railgrid.ai",
			Resources: []ProviderRequiredResource{{Name: "repositories/commit",
				Selector: &ProviderLabelSelector{MatchLabels: map[string]string{"a": "b"}}}}},
		want: "selector does not apply",
	}, {
		name: "a data-plane verb is not a Kubernetes verb",
		requirement: ProviderRequirement{Provider: "infrastructure", Group: "infrastructure.railgrid.ai",
			Resources: []ProviderRequiredResource{{Name: "instances", Verbs: []ProviderRequiredVerb{"exec"}}}},
		want: "not an ordinary Kubernetes verb",
	}, {
		name: "the group is not an API group",
		requirement: ProviderRequirement{Provider: "code", Group: "Code.Railgrid.AI",
			Resources: []ProviderRequiredResource{{Name: "repositories", Verbs: []ProviderRequiredVerb{"get"}}}},
		want: "DNS-subdomain API group",
	}, {
		name: "a provider requirement names the group it serves",
		requirement: ProviderRequirement{Provider: "code",
			Resources: []ProviderRequiredResource{{Name: "repositories", Verbs: []ProviderRequiredVerb{"get"}}}},
		want: "must name the API group",
	}, {
		name: "core secrets without a selector reach every Secret",
		requirement: ProviderRequirement{
			Resources: []ProviderRequiredResource{{Name: "secrets", Verbs: []ProviderRequiredVerb{"get"}}}},
		want: "must carry a selector",
	}, {
		// kcp refuses an APIExport whose claim on a custom subresource has no
		// claim on the kind that serves it, so the contract refuses it first.
		name: "a verb coordinate without its parent kind",
		requirement: ProviderRequirement{Provider: "code", Group: "code.railgrid.ai",
			Resources: []ProviderRequiredResource{
				{Name: "repositories", Verbs: []ProviderRequiredVerb{"get"}},
				{Name: "connections/mint-registry-token"},
			}},
		want: "needs its parent",
	}, {
		// Declaration order is not the author's problem: the parent may come
		// after the coordinate that needs it.
		name: "a verb coordinate declared before its parent is fine",
		requirement: ProviderRequirement{Provider: "code", Group: "code.railgrid.ai",
			Resources: []ProviderRequiredResource{
				{Name: "connections/mint-registry-token"},
				{Name: "connections", Verbs: []ProviderRequiredVerb{"get"}},
				{Name: "repositories/commit"},
			}},
		want: "needs its parent",
	}, {
		name: "duplicate coordinate",
		requirement: ProviderRequirement{Provider: "code", Group: "code.railgrid.ai",
			Resources: []ProviderRequiredResource{
				{Name: "repositories", Verbs: []ProviderRequiredVerb{"get"}},
				{Name: "repositories", Verbs: []ProviderRequiredVerb{"list"}},
			}},
		want: "duplicate coordinate",
	}, {
		name: "duplicate verb",
		requirement: ProviderRequirement{Provider: "code", Group: "code.railgrid.ai",
			Resources: []ProviderRequiredResource{{Name: "repositories", Verbs: []ProviderRequiredVerb{"get", "get"}}}},
		want: "listed twice",
	}} {
		t.Run(tc.name, func(t *testing.T) {
			if err := ValidateProviderRequirements([]ProviderRequirement{tc.requirement}); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("validation error = %v, want substring %q", err, tc.want)
			}
		})
	}
}

// One group belongs to one provider, so everything needed from it is stated
// once. Two entries would be two half-answers for the generator to merge.
func TestValidateProviderRequirementsRejectsARepeatedGroup(t *testing.T) {
	err := ValidateProviderRequirements([]ProviderRequirement{{
		Provider:  "code",
		Group:     "code.railgrid.ai",
		Resources: []ProviderRequiredResource{{Name: "repositories", Verbs: []ProviderRequiredVerb{"get"}}},
	}, {
		Provider:  "code",
		Group:     "code.railgrid.ai",
		Resources: []ProviderRequiredResource{{Name: "repositorycommits", Verbs: []ProviderRequiredVerb{"get"}}},
	}})
	if err == nil || !strings.Contains(err.Error(), "declared twice") {
		t.Fatalf("validation error = %v, want a repeated group", err)
	}
}

func TestDependenciesAreTheProvidersNamed(t *testing.T) {
	got := Dependencies(appStudioRequirements())
	if strings.Join(got, ",") != "infrastructure,code" {
		t.Fatalf("Dependencies() = %v, want infrastructure and code in declaration order", got)
	}
	// A builtin is not a dependency: nothing has to enable authorization.k8s.io.
	for _, name := range got {
		if name == "" {
			t.Fatal("a builtin requirement produced an empty dependency")
		}
	}
}

func TestRequiredCoordinatesFlattensEveryClaim(t *testing.T) {
	got := RequiredCoordinates(appStudioRequirements())
	if len(got) != 10 {
		t.Fatalf("RequiredCoordinates() returned %d claims, want 10: %+v", len(got), got)
	}
	var verbCoordinates, selectors int
	for _, claim := range got {
		if claim.Coordinate {
			verbCoordinates++
			if len(claim.Verbs) != 0 {
				t.Fatalf("a verb coordinate carried verbs: %+v", claim)
			}
		}
		if claim.Selector != nil {
			selectors++
		}
	}
	if verbCoordinates != 4 {
		t.Fatalf("flattened %d verb coordinates, want 4", verbCoordinates)
	}
	if selectors != 1 {
		t.Fatalf("flattened %d selectors, want the one on core secrets", selectors)
	}
	if got[0].Group != "infrastructure.railgrid.ai" || got[0].Provider != "infrastructure" {
		t.Fatalf("a claim must carry the group and provider it came from: %+v", got[0])
	}
}

func TestRequiredVerbStrings(t *testing.T) {
	got := RequiredVerbStrings([]ProviderRequiredVerb{"get", "list"})
	if strings.Join(got, ",") != "get,list" {
		t.Fatalf("RequiredVerbStrings() = %v", got)
	}
	if RequiredVerbStrings(nil) != nil {
		t.Fatal("RequiredVerbStrings(nil) must be nil, so an empty claim carries no verbs key")
	}
}
