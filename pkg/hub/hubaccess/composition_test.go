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

package hubaccess

import (
	"testing"

	providersv1alpha1 "github.com/railgrid/railgrid/apis/providers/v1alpha1"
	tenancyv1alpha1 "github.com/railgrid/railgrid/apis/tenancy/v1alpha1"
)

var instances = CompositionRequirement{Dependency: "infrastructure", Group: "infrastructure.railgrid.ai", Resource: "instances"}

func TestComposeCapabilityRoundTrips(t *testing.T) {
	capability := ComposeCapability("infrastructure.railgrid.ai", "instances")
	if capability != "compose:infrastructure.railgrid.ai/instances" {
		t.Fatalf("capability = %q", capability)
	}
	group, resource, ok := ParseComposeCapability(capability)
	if !ok || group != "infrastructure.railgrid.ai" || resource != "instances" {
		t.Fatalf("parse = (%q, %q, %v)", group, resource, ok)
	}
	// A hub-access capability is not a composition and must not be read as one.
	if _, _, ok := ParseComposeCapability("memberships.read"); ok {
		t.Fatal("memberships.read parsed as a composition")
	}
	if _, _, ok := ParseComposeCapability("compose:instances"); ok {
		t.Fatal("a capability with no group parsed as a composition")
	}
}

// A requirement on another provider names either a kind or one verb on a kind,
// and both are recorded as compositions. A coordinate that did not parse back
// would be offered for consent, recorded in the Grant, and then missing from
// every listing that reads the Grant.
func TestComposeCapabilityRoundTripsAVerbCoordinate(t *testing.T) {
	capability := ComposeCapability("code.railgrid.ai", "repositories/commit")
	if capability != "compose:code.railgrid.ai/repositories/commit" {
		t.Fatalf("capability = %q", capability)
	}
	group, resource, ok := ParseComposeCapability(capability)
	if !ok || group != "code.railgrid.ai" || resource != "repositories/commit" {
		t.Fatalf("parse = (%q, %q, %v), want the coordinate intact", group, resource, ok)
	}
	// Nothing in the contract names anything deeper than a coordinate.
	if _, _, ok := ParseComposeCapability("compose:code.railgrid.ai/repositories/commit/extra"); ok {
		t.Fatal("a three-segment capability parsed as a composition")
	}
}

// A composition is a requirement that NAMES a provider. A requirement on a
// platform builtin belongs to nobody, so there is no "this provider manages
// that provider's kinds here" consent to offer for it and it must not appear.
func TestDeclaredCompositionsFlattensRequirements(t *testing.T) {
	get := []providersv1alpha1.ProviderRequiredVerb{providersv1alpha1.RequiredVerbGet}
	declared := DeclaredCompositions([]providersv1alpha1.ProviderRequirement{{
		Provider: "infrastructure",
		Group:    "infrastructure.railgrid.ai",
		Resources: []providersv1alpha1.ProviderRequiredResource{
			{Name: "instances", Verbs: get},
		},
	}, {
		Provider: "code",
		Group:    "code.railgrid.ai",
		Resources: []providersv1alpha1.ProviderRequiredResource{
			{Name: "repositories", Verbs: get},
			{Name: "repositorycommits", Verbs: get},
		},
	}, {
		Group: "authorization.k8s.io",
		Resources: []providersv1alpha1.ProviderRequiredResource{
			{Name: "subjectaccessreviews", Verbs: []providersv1alpha1.ProviderRequiredVerb{providersv1alpha1.RequiredVerbCreate}},
		},
	}})
	if len(declared) != 3 || declared[0] != instances || declared[2].Dependency != "code" {
		t.Fatalf("declared = %+v", declared)
	}
}

func TestComposeAllowed(t *testing.T) {
	accepted := &tenancyv1alpha1.Grant{Spec: tenancyv1alpha1.GrantSpec{
		Capabilities: []tenancyv1alpha1.GrantedCapability{GrantedComposition(instances)},
	}}
	declined := &tenancyv1alpha1.Grant{Spec: tenancyv1alpha1.GrantSpec{
		Declined: []tenancyv1alpha1.CapabilityRef{instances.Ref()},
	}}
	for _, tc := range []struct {
		name             string
		grant            *tenancyv1alpha1.Grant
		platformProvider bool
		platformDefault  bool
		wantAllowed      bool
		wantByDefault    bool
	}{
		{name: "accepted", grant: accepted, wantAllowed: true},
		{name: "declined", grant: declined, platformProvider: true, platformDefault: true},
		{name: "undecided, no default", grant: nil, platformProvider: true},
		{name: "undecided platform provider under the default", grant: nil, platformProvider: true, platformDefault: true, wantAllowed: true, wantByDefault: true},
		{name: "undecided org-owned provider is never defaulted", grant: nil, platformDefault: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			allowed, byDefault := ComposeAllowed(tc.grant, instances, tc.platformProvider, tc.platformDefault)
			if allowed != tc.wantAllowed || byDefault != tc.wantByDefault {
				t.Fatalf("= (%v, %v), want (%v, %v)", allowed, byDefault, tc.wantAllowed, tc.wantByDefault)
			}
		})
	}
}

// A composition is a workspace decision: a workspace admin is enough, a plain
// member never is.
func TestMayDecideComposition(t *testing.T) {
	for _, tc := range []struct {
		ws, org string
		want    bool
	}{
		{ws: "admin", org: "member", want: true},
		{ws: "member", org: "admin", want: true},
		{ws: "member", org: "member"},
		{ws: "", org: ""},
	} {
		if got := MayDecideComposition(tc.ws, tc.org); got != tc.want {
			t.Fatalf("MayDecideComposition(%q, %q) = %v, want %v", tc.ws, tc.org, got, tc.want)
		}
	}
}
