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
	"encoding/base64"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	providersv1alpha1 "github.com/railgrid/railgrid/apis/providers/v1alpha1"
	tenancyv1alpha1 "github.com/railgrid/railgrid/apis/tenancy/v1alpha1"
	"github.com/railgrid/railgrid/pkg/hub/actionproof"
	"github.com/railgrid/railgrid/pkg/hub/providers"
	"github.com/railgrid/railgrid/pkg/hub/serviceaccounts"
	"github.com/railgrid/railgrid/pkg/hub/tenant"
)

type actionProofProviderLookup struct{ provider providers.Provider }

func (f actionProofProviderLookup) Get(name string) (providers.Provider, bool) {
	return f.provider, f.provider.Name == name
}

func (f actionProofProviderLookup) GetForOrg(orgUUID, name string) (providers.Provider, bool) {
	provider, ok := f.Get(name)
	return provider, ok && provider.OrgUUID == orgUUID
}

type actionProofGrantReader struct{}

func (actionProofGrantReader) Get(_ context.Context, _ GrantKey) (*tenancyv1alpha1.Grant, error) {
	return &tenancyv1alpha1.Grant{Spec: tenancyv1alpha1.GrantSpec{Capabilities: []tenancyv1alpha1.GrantedCapability{
		{Capability: "memberships.read", Scope: "org"},
		{Capability: "memberships.read", Scope: "workspace"},
		{Capability: "memberships.invite", Scope: "org", MaxRole: "member", AllowInvite: true},
	}}}, nil
}

func TestActionProofGateBindsMembershipCallsToAppStudioAction(t *testing.T) {
	key := serviceaccounts.StaticProofKeySource(strings.Repeat("a", 32))
	now := time.Date(2026, time.October, 7, 12, 0, 0, 0, time.UTC)
	claims := actionproof.Claims{
		UserID: "user-alice", RBACIdentity: "railgrid:alice@example.com",
		OrgUUID: "org", WorkspaceUUID: "workspace", ClusterID: "cluster-id",
		Provider: "app-studio", Group: "ai.railgrid.ai", Version: "v1alpha1",
		Resource: "projects", ParentName: "project-1", Verb: "publishing-grants", Method: http.MethodPost,
	}
	providerToken := actionProofServiceAccountToken()
	readInvite := []providersv1alpha1.ProviderHubAccess{
		{Capability: providersv1alpha1.HubCapabilityMembershipsRead, Scope: providersv1alpha1.HubAccessScopeOrg},
		{Capability: providersv1alpha1.HubCapabilityMembershipsRead, Scope: providersv1alpha1.HubAccessScopeWorkspace},
		{Capability: providersv1alpha1.HubCapabilityMembershipsInvite, Scope: providersv1alpha1.HubAccessScopeOrg, MaxRole: "member", AllowInvite: true},
	}
	human := tenant.UserResolverFunc(func(*http.Request) (string, error) { return "", errors.New("provider token is not a human credential") })
	gate := &Gate{
		Human: human,
		ActionProof: &actionproof.Verifier{
			Keys: key,
			Authenticate: func(_ context.Context, r *http.Request, c actionproof.Claims) error {
				if r.Header.Get("Authorization") != "Bearer "+providerToken || c.Provider != "app-studio" {
					return errors.New("wrong provider credential")
				}
				return nil
			},
			ValidateScope: func(_ context.Context, c actionproof.Claims) error {
				if c.OrgUUID != "org" || c.WorkspaceUUID != "workspace" {
					return errors.New("wrong tenant")
				}
				return nil
			},
			Now: func() time.Time { return now.Add(time.Second) },
		},
		Providers: actionProofProviderLookup{provider: providers.Provider{Name: "app-studio", HubAccess: readInvite}},
		Grants:    actionProofGrantReader{},
	}

	for _, tc := range []struct {
		name, method, path, verb, sourceMethod string
		inviteUser, body                       string
		wantStatus                             int
		wantCapability                         string
	}{
		{name: "grant write may read org roster", method: http.MethodGet, path: "/api/orgs/org/memberships", verb: "publishing-grants", sourceMethod: http.MethodPost, inviteUser: "alice@example.com", wantCapability: "memberships.read"},
		{name: "grant write may read matching workspace roster", method: http.MethodGet, path: "/api/orgs/org/workspaces/workspace/memberships", verb: "publishing-grants", sourceMethod: http.MethodPost, inviteUser: "alice@example.com", wantCapability: "memberships.read"},
		{name: "grant write may invite", method: http.MethodPost, path: "/api/orgs/org/memberships", verb: "publishing-grants", sourceMethod: http.MethodPost, inviteUser: "alice@example.com", body: `{"user":"alice@example.com","role":"member","invite":true}`, wantCapability: "memberships.invite"},
		{name: "preview grant may invite", method: http.MethodPost, path: "/api/orgs/org/memberships", verb: "preview-grants", sourceMethod: http.MethodPost, inviteUser: "alice@example.com", body: `{"user":"alice@example.com","role":"member","invite":true}`, wantCapability: "memberships.invite"},
		{name: "read action cannot invite", method: http.MethodPost, path: "/api/orgs/org/memberships", verb: "publishing-grants", sourceMethod: http.MethodGet, body: `{"user":"alice@example.com","role":"member","invite":true}`, wantStatus: http.StatusForbidden},
		{name: "missing signed recipient", method: http.MethodPost, path: "/api/orgs/org/memberships", verb: "publishing-grants", sourceMethod: http.MethodPost, body: `{"user":"alice@example.com","role":"member","invite":true}`, wantStatus: http.StatusForbidden},
		{name: "different recipient", method: http.MethodPost, path: "/api/orgs/org/memberships", verb: "publishing-grants", sourceMethod: http.MethodPost, inviteUser: "alice@example.com", body: `{"user":"bob@example.com","role":"member","invite":true}`, wantStatus: http.StatusForbidden},
		{name: "wrong role", method: http.MethodPost, path: "/api/orgs/org/memberships", verb: "publishing-grants", sourceMethod: http.MethodPost, inviteUser: "alice@example.com", body: `{"user":"alice@example.com","role":"admin","invite":true}`, wantStatus: http.StatusForbidden},
		{name: "invite flag missing", method: http.MethodPost, path: "/api/orgs/org/memberships", verb: "publishing-grants", sourceMethod: http.MethodPost, inviteUser: "alice@example.com", body: `{"user":"alice@example.com","role":"member"}`, wantStatus: http.StatusForbidden},
		{name: "unknown body field", method: http.MethodPost, path: "/api/orgs/org/memberships", verb: "publishing-grants", sourceMethod: http.MethodPost, inviteUser: "alice@example.com", body: `{"user":"alice@example.com","role":"member","invite":true,"extra":true}`, wantStatus: http.StatusForbidden},
		{name: "trailing JSON", method: http.MethodPost, path: "/api/orgs/org/memberships", verb: "publishing-grants", sourceMethod: http.MethodPost, inviteUser: "alice@example.com", body: `{"user":"alice@example.com","role":"member","invite":true}{}`, wantStatus: http.StatusForbidden},
		{name: "oversized body", method: http.MethodPost, path: "/api/orgs/org/memberships", verb: "publishing-grants", sourceMethod: http.MethodPost, inviteUser: "alice@example.com", body: `{"user":"alice@example.com","role":"member","invite":true,"padding":"` + strings.Repeat("a", 64<<10) + `"}`, wantStatus: http.StatusForbidden},
		{name: "wrong org", method: http.MethodGet, path: "/api/orgs/other/memberships", verb: "publishing-grants", sourceMethod: http.MethodPost, wantStatus: http.StatusForbidden},
		{name: "wrong workspace", method: http.MethodGet, path: "/api/orgs/org/workspaces/other/memberships", verb: "publishing-grants", sourceMethod: http.MethodPost, wantStatus: http.StatusForbidden},
		{name: "unrelated action", method: http.MethodGet, path: "/api/orgs/org/memberships", verb: "publishing", sourceMethod: http.MethodGet, wantStatus: http.StatusForbidden},
	} {
		t.Run(tc.name, func(t *testing.T) {
			caseClaims := claims
			caseClaims.Verb = tc.verb
			caseClaims.Method = tc.sourceMethod
			caseClaims.InviteUser = tc.inviteUser
			caseToken, err := actionproof.Sign(context.Background(), key, caseClaims, now)
			if err != nil {
				t.Fatal(err)
			}
			request := httptest.NewRequest(tc.method, tc.path, strings.NewReader(tc.body))
			request.Header.Set("Authorization", "Bearer "+providerToken)
			request.Header.Set(actionproof.Header, caseToken)
			request.Header.Set("X-Railgrid-Org", claims.OrgUUID)
			request.Header.Set("X-Railgrid-Workspace", claims.WorkspaceUUID)
			request.Header.Set("X-Railgrid-Tenant", claims.ClusterID)
			request.Header.Set("X-Railgrid-Cluster", claims.ClusterID)
			var gotCall tenant.DelegatedCall
			var gotUser string
			var gotBody []byte
			reached := false
			handler := gate.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				reached = true
				gotCall, _ = tenant.DelegatedCallFrom(r.Context())
				gotUser, _ = DelegatedUserResolver(human).ResolveUser(r)
				if r.Method == http.MethodPost {
					gotBody, _ = io.ReadAll(r.Body)
				}
			}))
			recorder := httptest.NewRecorder()
			handler.ServeHTTP(recorder, request)
			if tc.wantStatus != 0 {
				if reached || recorder.Code != tc.wantStatus {
					t.Fatalf("status=%d reached=%v body=%s, want %d", recorder.Code, reached, recorder.Body.String(), tc.wantStatus)
				}
				return
			}
			if !reached || gotUser != claims.UserID || !gotCall.ActionProof || gotCall.Capability != tc.wantCapability {
				t.Fatalf("reached=%v user=%q call=%+v status=%d; want proof caller and %s", reached, gotUser, gotCall, recorder.Code, tc.wantCapability)
			}
			if tc.method == http.MethodPost && string(gotBody) != tc.body {
				t.Fatalf("forwarded POST body = %q, want unchanged %q", gotBody, tc.body)
			}
		})
	}
}

func actionProofServiceAccountToken() string {
	enc := base64.RawURLEncoding.EncodeToString
	return enc([]byte(`{"alg":"RS256"}`)) + "." +
		enc([]byte(`{"iss":"https://kcp.default.svc","kubernetes.io":{"namespace":"default","serviceaccount":{"name":"railgrid-provider"}}}`)) + ".sig"
}
