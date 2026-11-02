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
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	providersv1alpha1 "github.com/railgrid/railgrid/apis/providers/v1alpha1"
	tenancyv1alpha1 "github.com/railgrid/railgrid/apis/tenancy/v1alpha1"
	"github.com/railgrid/railgrid/pkg/hub/providers"
	"github.com/railgrid/railgrid/pkg/hub/tenant"
)

type orgBackendTestProviderLookup struct{ provider providers.Provider }

func (f orgBackendTestProviderLookup) Get(name string) (providers.Provider, bool) {
	return f.provider, f.provider.Name == name
}

func (f orgBackendTestProviderLookup) GetForOrg(orgUUID, name string) (providers.Provider, bool) {
	return f.provider, f.provider.Name == name && f.provider.OrgUUID == orgUUID
}

type orgBackendTestGrants struct {
	getCalls       int
	uncachedCalls  int
	uncachedResult *tenancyv1alpha1.Grant
}

func (g *orgBackendTestGrants) Get(context.Context, GrantKey) (*tenancyv1alpha1.Grant, error) {
	g.getCalls++
	return nil, errors.New("cached grant path must not be used for BYO")
}

func (g *orgBackendTestGrants) GetUncached(context.Context, GrantKey) (*tenancyv1alpha1.Grant, error) {
	g.uncachedCalls++
	return g.uncachedResult, nil
}

func TestOrgBackendTokenUsesLiveGrantAndCannotFallThroughToHuman(t *testing.T) {
	grants := &orgBackendTestGrants{uncachedResult: &tenancyv1alpha1.Grant{Spec: tenancyv1alpha1.GrantSpec{Capabilities: []tenancyv1alpha1.GrantedCapability{
		{Capability: "memberships.read", Scope: "org"},
	}}}}
	humanCalls, verifyCalls, nextCalls := 0, 0, 0
	gate := &Gate{
		Human: tenant.UserResolverFunc(func(*http.Request) (string, error) { humanCalls++; return "attacker", nil }),
		Verify: func(r *http.Request) (Caller, error) {
			verifyCalls++
			if got := r.Header.Get("Authorization"); got != "Bearer rgbyo1.signed" {
				return Caller{}, errors.New("wrong bearer")
			}
			return Caller{User: "user-alice", OrgUUID: "org-a", WorkspaceUUID: "team-a", Delegated: true, Provider: "custom", ProviderOrgUUID: "org-a"}, nil
		},
		Providers: orgBackendTestProviderLookup{provider: providers.Provider{
			Name: "custom", OrgUUID: "org-a",
			HubAccess: []providersv1alpha1.ProviderHubAccess{{Capability: providersv1alpha1.HubCapabilityMembershipsRead, Scope: providersv1alpha1.HubAccessScopeOrg}},
		}},
		Grants: grants,
	}
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		nextCalls++
		call, ok := tenant.DelegatedCallFrom(r.Context())
		if !ok || call.User != "user-alice" || call.ProviderOrgUUID != "org-a" {
			t.Errorf("delegated call = %+v, ok=%v", call, ok)
		}
		w.WriteHeader(http.StatusNoContent)
	})
	request := httptest.NewRequest(http.MethodGet, "/api/orgs/org-a/memberships", nil)
	request.Header.Set("Authorization", "Bearer rgbyo1.signed")
	recorder := httptest.NewRecorder()
	gate.Middleware(next).ServeHTTP(recorder, request)
	if recorder.Code != http.StatusNoContent || humanCalls != 0 || verifyCalls != 1 || nextCalls != 1 {
		t.Fatalf("status=%d human=%d verify=%d next=%d", recorder.Code, humanCalls, verifyCalls, nextCalls)
	}
	if grants.getCalls != 0 || grants.uncachedCalls != 1 {
		t.Fatalf("grant reads cached=%d uncached=%d; want 0 and 1", grants.getCalls, grants.uncachedCalls)
	}
}

func TestInvalidOrgBackendTokenIsTerminalAtHubAccessGate(t *testing.T) {
	humanCalls, nextCalls := 0, 0
	gate := &Gate{
		Human:  tenant.UserResolverFunc(func(*http.Request) (string, error) { humanCalls++; return "user-alice", nil }),
		Verify: func(*http.Request) (Caller, error) { return Caller{}, errors.New("bad signature") },
	}
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/api/orgs/org-a/memberships", nil)
	request.Header.Set("Authorization", "Bearer rgbyo1.tampered")
	gate.Middleware(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { nextCalls++ })).ServeHTTP(recorder, request)
	if recorder.Code != http.StatusUnauthorized || humanCalls != 0 || nextCalls != 0 {
		t.Fatalf("status=%d human=%d next=%d; opaque failure must not fall through", recorder.Code, humanCalls, nextCalls)
	}
}
