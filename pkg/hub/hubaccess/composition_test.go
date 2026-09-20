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

	tenancyv1alpha1 "github.com/railgrid/railgrid/apis/tenancy/v1alpha1"
	"github.com/railgrid/railgrid/pkg/hub/providers"
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

func TestDeclaredCompositionsFlattensDependencies(t *testing.T) {
	declared := DeclaredCompositions([]providers.Dependency{
		{Name: "infrastructure", Composes: []providers.Composition{{Group: "infrastructure.railgrid.ai", Resource: "instances"}}},
		{Name: "code", Composes: []providers.Composition{
			{Group: "code.railgrid.ai", Resource: "repositories"},
			{Group: "code.railgrid.ai", Resource: "repositorycommits"},
		}},
		{Name: "quickstart"},
	})
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
