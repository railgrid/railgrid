/*
Copyright 2026 The Faros Authors.

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
	"context"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"

	providersv1alpha1 "github.com/faroshq/faros/apis/providers/v1alpha1"
	tenancyv1alpha1 "github.com/faroshq/faros/apis/tenancy/v1alpha1"
	farosclient "github.com/faroshq/faros/pkg/client"
)

func TestMatchIsAClosedSet(t *testing.T) {
	for _, tc := range []struct {
		method, path string
		want         *Requirement
	}{
		{http.MethodGet, "/api/orgs/o/memberships", &Requirement{providersv1alpha1.HubCapabilityMembershipsRead, providersv1alpha1.HubAccessScopeOrg}},
		{http.MethodGet, "/api/orgs/o/workspaces/w/memberships", &Requirement{providersv1alpha1.HubCapabilityMembershipsRead, providersv1alpha1.HubAccessScopeWorkspace}},
		{http.MethodPost, "/api/orgs/o/memberships", &Requirement{providersv1alpha1.HubCapabilityMembershipsInvite, providersv1alpha1.HubAccessScopeOrg}},
		{http.MethodGet, "/api/orgs/o/memberships/../memberships", &Requirement{providersv1alpha1.HubCapabilityMembershipsRead, providersv1alpha1.HubAccessScopeOrg}},
		{http.MethodPost, "/api/orgs/o/workspaces/w/memberships", nil},
		{http.MethodPatch, "/api/orgs/o/memberships/u", nil},
		{http.MethodDelete, "/api/orgs/o/memberships/u", nil},
		{http.MethodDelete, "/api/orgs/o/memberships/me", nil},
		{http.MethodGet, "/api/orgs/o", nil},
		{http.MethodGet, "/api/orgs/o/workspaces", nil},
		{http.MethodPost, "/api/orgs/o/workspaces/w/providers/x/enable", nil},
		{http.MethodGet, "/api/orgs//memberships", nil},
	} {
		got, ok := Match(tc.method, tc.path)
		if tc.want == nil {
			if ok {
				t.Errorf("%s %s matched %+v, want no capability", tc.method, tc.path, got)
			}
			continue
		}
		if !ok || got != *tc.want {
			t.Errorf("%s %s = %+v/%v, want %+v", tc.method, tc.path, got, ok, *tc.want)
		}
	}
}

func TestEffectiveNeverWidens(t *testing.T) {
	declared := providersv1alpha1.ProviderHubAccess{Capability: providersv1alpha1.HubCapabilityMembershipsInvite, Scope: providersv1alpha1.HubAccessScopeOrg, AllowInvite: false}
	granted := tenancyv1alpha1.GrantedCapability{Capability: "memberships.invite", Scope: "org", MaxRole: "admin", AllowInvite: true}
	got := Effective(declared, granted)
	if got.MaxRole != tenancyv1alpha1.MembershipRoleMember || got.AllowInvite {
		t.Fatalf("Effective = %+v: an old grant widened a narrowed declaration", got)
	}
}

func TestBearerLooksLikeServiceAccount(t *testing.T) {
	enc := base64.RawURLEncoding.EncodeToString
	jwt := func(payload string) string { return enc([]byte(`{}`)) + "." + enc([]byte(payload)) + ".s" }
	for name, tc := range map[string]struct {
		auth string
		want bool
	}{
		"bound SA token":  {"Bearer " + jwt(`{"kubernetes.io":{"namespace":"default"}}`), true},
		"legacy SA token": {"Bearer " + jwt(`{"kubernetes.io/serviceaccount/namespace":"default"}`), true},
		"OIDC ID token":   {"Bearer " + jwt(`{"iss":"https://dex","email":"a@b"}`), false},
		"static token":    {"Bearer dev-token", false},
		"no bearer":       {"", false},
		"garbage":         {"Bearer a.!!!.c", false},
	} {
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		if tc.auth != "" {
			r.Header.Set("Authorization", tc.auth)
		}
		if got := bearerLooksLikeServiceAccount(r); got != tc.want {
			t.Errorf("%s: got %v, want %v", name, got, tc.want)
		}
	}
}

func TestWindowLimiter(t *testing.T) {
	now := time.Unix(0, 0)
	l := windowLimiter{now: func() time.Time { return now }}
	for i := 0; i < 3; i++ {
		if !l.allow("k", 3, time.Hour) {
			t.Fatalf("call %d refused under the limit", i)
		}
	}
	if l.allow("k", 3, time.Hour) {
		t.Fatal("fourth call within the window allowed")
	}
	if !l.allow("other", 3, time.Hour) {
		t.Fatal("limit leaked across keys")
	}
	now = now.Add(time.Hour)
	if !l.allow("k", 3, time.Hour) {
		t.Fatal("window did not reset")
	}
}

func newTestStore(t *testing.T) *Store {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := tenancyv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(scheme, map[schema.GroupVersionResource]string{farosclient.GrantGVR: "GrantList"})
	return NewStore(farosclient.NewFromDynamic(dyn))
}

func TestStorePutGetDelete(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	key := GrantKey{OrgUUID: "o", WorkspaceUUID: "w", Provider: "p"}
	if g, err := s.Get(ctx, key); err != nil || g != nil {
		t.Fatalf("Get before Put = %v, %v", g, err)
	}
	caps := []tenancyv1alpha1.GrantedCapability{{Capability: "memberships.read", Scope: "org"}}
	if _, err := s.Put(ctx, key, caps, nil, "alice"); err != nil {
		t.Fatal(err)
	}
	s.cache = map[string]cachedGrant{} // read through to the API
	g, err := s.Get(ctx, key)
	if err != nil || g == nil || len(g.Spec.Capabilities) != 1 || g.Spec.Subject.Kind != tenancyv1alpha1.GrantSubjectProvider || g.Spec.AcceptedBy != "alice" {
		t.Fatalf("Get after Put = %+v, %v", g, err)
	}
	if g.Labels[tenancyv1alpha1.LabelGrantSubjectName] != "p" || g.Labels[tenancyv1alpha1.LabelGrantWorkspace] != "w" {
		t.Fatalf("labels = %v", g.Labels)
	}
	// Put replaces the decisions.
	if _, err := s.Put(ctx, key, nil, []tenancyv1alpha1.CapabilityRef{{Capability: "memberships.read", Scope: "org"}}, "bob"); err != nil {
		t.Fatal(err)
	}
	s.cache = map[string]cachedGrant{}
	if g, _ := s.Get(ctx, key); g == nil || len(g.Spec.Capabilities) != 0 || len(g.Spec.Declined) != 1 || g.Spec.AcceptedBy != "bob" {
		t.Fatalf("Get after second Put = %+v", g)
	}
	// The owner is part of the identity: an org copy never reads the
	// platform provider's grant.
	if g, _ := s.Get(ctx, GrantKey{OrgUUID: "o", WorkspaceUUID: "w", Provider: "p", ProviderOrgUUID: "o"}); g != nil {
		t.Fatalf("org-owned key read the platform grant: %+v", g)
	}
	if err := s.Delete(ctx, key); err != nil {
		t.Fatal(err)
	}
	if err := s.Delete(ctx, key); err != nil {
		t.Fatalf("second Delete: %v", err)
	}
	s.cache = map[string]cachedGrant{}
	if g, _ := s.Get(ctx, key); g != nil {
		t.Fatalf("Get after Delete = %+v", g)
	}
}

// TestStoreRefusesMismatchedGrant: a grant object whose spec names another
// tuple than its (hash) name encodes is not trusted.
func TestStoreRefusesMismatchedGrant(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	key := GrantKey{OrgUUID: "o", WorkspaceUUID: "w", Provider: "p"}
	_, err := s.client.Grants().Create(ctx, &tenancyv1alpha1.Grant{
		ObjectMeta: metav1.ObjectMeta{Name: key.Name()},
		Spec: tenancyv1alpha1.GrantSpec{
			Subject:       tenancyv1alpha1.GrantSubject{Kind: tenancyv1alpha1.GrantSubjectProvider, Name: "someone-else"},
			OrgUUID:       "o",
			WorkspaceUUID: "w",
			Capabilities:  []tenancyv1alpha1.GrantedCapability{{Capability: "memberships.invite", Scope: "org"}},
		},
	}, metav1.CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if g, err := s.Get(ctx, key); err != nil || g != nil {
		t.Fatalf("mismatched grant trusted: %+v, %v", g, err)
	}
}

// TestRecordWithoutDecisionsWritesNothing: a caller who may decide nothing
// leaves every capability undecided rather than recording an empty grant.
func TestRecordWithoutDecisionsWritesNothing(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	key := GrantKey{OrgUUID: "o", WorkspaceUUID: "w", Provider: "p"}
	g, err := s.Record(ctx, key, func(*tenancyv1alpha1.Grant) ([]tenancyv1alpha1.GrantedCapability, []tenancyv1alpha1.CapabilityRef) {
		return nil, nil
	}, "carol")
	if err != nil || g != nil {
		t.Fatalf("Record with no decisions = %+v, %v; want nothing written", g, err)
	}
	if got, _ := s.client.Grants().List(ctx, metav1.ListOptions{}); got != nil && len(got.Items) != 0 {
		t.Fatalf("grants written: %+v", got.Items)
	}
}

func TestAllowed(t *testing.T) {
	read := providersv1alpha1.ProviderHubAccess{Capability: providersv1alpha1.HubCapabilityMembershipsRead, Scope: providersv1alpha1.HubAccessScopeOrg}
	grant := func(accepted bool, declined bool) *tenancyv1alpha1.Grant {
		g := &tenancyv1alpha1.Grant{}
		if accepted {
			g.Spec.Capabilities = []tenancyv1alpha1.GrantedCapability{{Capability: "memberships.read", Scope: "org"}}
		}
		if declined {
			g.Spec.Declined = []tenancyv1alpha1.CapabilityRef{{Capability: "memberships.read", Scope: "org"}}
		}
		return g
	}
	for _, tc := range []struct {
		name                      string
		grant                     *tenancyv1alpha1.Grant
		platform, platformDefault bool
		want, byDefault           bool
	}{
		{"accepted, org-owned", grant(true, false), false, false, true, false},
		{"declined beats the platform default", grant(false, true), true, true, false, false},
		{"undecided platform provider, default on", grant(false, false), true, true, true, true},
		{"no grant at all, default on", nil, true, true, true, true},
		{"undecided platform provider, default off", grant(false, false), true, false, false, false},
		{"undecided org-owned provider never defaults", nil, false, true, false, false},
	} {
		_, ok, byDefault := Allowed(tc.grant, read, tc.platform, tc.platformDefault)
		if ok != tc.want || byDefault != tc.byDefault {
			t.Errorf("%s: Allowed = %v (byDefault %v), want %v (%v)", tc.name, ok, byDefault, tc.want, tc.byDefault)
		}
	}
}
