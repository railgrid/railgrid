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

package proxy

import (
	"context"
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/klog/v2"

	tenancyv1alpha1 "github.com/railgrid/railgrid/apis/tenancy/v1alpha1"
	railgridclient "github.com/railgrid/railgrid/pkg/client"
	"github.com/railgrid/railgrid/pkg/util/identity"
)

// A hex token is exactly what the docs generate (openssl rand -hex 32) and
// what the old slug scheme copied verbatim into the email and display name.
const testStaticToken = "0f3c9a7be21d4c56a8e9f01b2c3d4e5f60718293a4b5c6d7e8f9011223344556"

func newStaticTokenTestProxy(t *testing.T, tokens ...string) *KCPProxy {
	t.Helper()
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(),
		map[schema.GroupVersionResource]string{
			railgridclient.UserGVR:                "UserList",
			railgridclient.OrganizationGVR:        "OrganizationList",
			railgridclient.UserMembershipIndexGVR: "UserMembershipIndexList",
		})
	return &KCPProxy{
		railgridClient:   railgridclient.NewFromDynamic(dyn),
		staticAuthTokens: tokens,
		logger:           klog.Background(),
	}
}

// seedLegacyStaticTokenUser creates a User the way hubs before this change
// did: email and display name built from the token text. A non-empty
// personalOrg is recorded in its status.
func seedLegacyStaticTokenUser(t *testing.T, p *KCPProxy, token, personalOrg string) {
	t.Helper()
	id := identity.NewStaticToken(token)
	_, err := p.railgridClient.Users().Create(context.Background(), &tenancyv1alpha1.User{
		ObjectMeta: metav1.ObjectMeta{
			Name: id.UserName,
			Labels: map[string]string{
				"tenants.railgrid.ai/sub":       id.Sub,
				"tenants.railgrid.ai/auth-type": "static-token",
			},
		},
		Spec: tenancyv1alpha1.UserSpec{
			Email:        "static-" + token + "@railgrid.local",
			Name:         "Static Token User (" + token + ")",
			RBACIdentity: id.RBACIdentity,
		},
		Status: tenancyv1alpha1.UserStatus{PersonalOrg: personalOrg},
	}, metav1.CreateOptions{})
	if err != nil {
		t.Fatalf("seeding legacy user: %v", err)
	}
}

func assertShowsOnlyRBACIdentity(t *testing.T, user *tenancyv1alpha1.User, token string) {
	t.Helper()
	id := identity.NewStaticToken(token)
	if user.Spec.Email != "" {
		t.Errorf("email = %q, want empty", user.Spec.Email)
	}
	if user.Spec.Name != id.RBACIdentity {
		t.Errorf("display name = %q, want the RBAC identity %q", user.Spec.Name, id.RBACIdentity)
	}
	if user.Spec.RBACIdentity != id.RBACIdentity {
		t.Errorf("rbacIdentity = %q, want %q", user.Spec.RBACIdentity, id.RBACIdentity)
	}
	for field, v := range map[string]string{"email": user.Spec.Email, "name": user.Spec.Name, "metadata.name": user.Name} {
		if strings.Contains(v, token) {
			t.Errorf("%s %q contains the token", field, v)
		}
	}
}

func getStaticTokenUser(t *testing.T, p *KCPProxy, token string) *tenancyv1alpha1.User {
	t.Helper()
	user, err := p.railgridClient.Users().Get(context.Background(), identity.NewStaticToken(token).UserName, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("getting user: %v", err)
	}
	return user
}

func TestStaticTokenUserCarriesNoTokenText(t *testing.T) {
	p := newStaticTokenTestProxy(t, testStaticToken)
	user, err := p.ensureStaticTokenUser(context.Background(), identity.NewStaticToken(testStaticToken))
	if err != nil {
		t.Fatalf("ensureStaticTokenUser: %v", err)
	}
	assertShowsOnlyRBACIdentity(t, user, testStaticToken)
	assertShowsOnlyRBACIdentity(t, getStaticTokenUser(t, p, testStaticToken), testStaticToken)
}

func TestStaticTokenUserRewrittenOnLogin(t *testing.T) {
	p := newStaticTokenTestProxy(t, testStaticToken)
	seedLegacyStaticTokenUser(t, p, testStaticToken, "")

	user, err := p.ensureStaticTokenUser(context.Background(), identity.NewStaticToken(testStaticToken))
	if err != nil {
		t.Fatalf("ensureStaticTokenUser: %v", err)
	}
	assertShowsOnlyRBACIdentity(t, user, testStaticToken)
	assertShowsOnlyRBACIdentity(t, getStaticTokenUser(t, p, testStaticToken), testStaticToken)
}

func TestScrubStaticTokenUsers(t *testing.T) {
	const unused = "never-logged-in"
	p := newStaticTokenTestProxy(t, "", testStaticToken, unused)
	seedLegacyStaticTokenUser(t, p, testStaticToken, "")

	if err := p.ScrubStaticTokenUsers(context.Background()); err != nil {
		t.Fatalf("ScrubStaticTokenUsers: %v", err)
	}
	assertShowsOnlyRBACIdentity(t, getStaticTokenUser(t, p, testStaticToken), testStaticToken)

	// The startup pass rewrites; it must never create Users for tokens
	// nobody has logged in with.
	users, err := p.railgridClient.Users().List(context.Background(), metav1.ListOptions{})
	if err != nil {
		t.Fatalf("listing users: %v", err)
	}
	if len(users.Items) != 1 {
		t.Fatalf("got %d users after scrub, want 1", len(users.Items))
	}

	// Idempotent: a second pass has nothing to do.
	if err := p.ScrubStaticTokenUsers(context.Background()); err != nil {
		t.Fatalf("second ScrubStaticTokenUsers: %v", err)
	}
}

// The organization controller named the personal Org after the User's old
// display name, and every member's index row copied that name. Both carry the
// token and must be rewritten; a name the owner chose stays.
func TestScrubStaticTokenPersonalOrg(t *testing.T) {
	ctx := context.Background()
	id := identity.NewStaticToken(testStaticToken)
	legacyOrgName := "Static Token User (" + testStaticToken + ")'s personal"
	wantOrgName := id.RBACIdentity + "'s personal"

	for _, tc := range []struct {
		name     string
		orgName  string
		wantName string
	}{
		{name: "default name is rewritten", orgName: legacyOrgName, wantName: wantOrgName},
		{name: "owner-chosen name is kept", orgName: "Lab", wantName: "Lab"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := newStaticTokenTestProxy(t, testStaticToken)
			seedLegacyStaticTokenUser(t, p, testStaticToken, "org-personal")
			if _, err := p.railgridClient.Organizations().Create(ctx, &tenancyv1alpha1.Organization{
				ObjectMeta: metav1.ObjectMeta{Name: "org-personal"},
				Spec:       tenancyv1alpha1.OrganizationSpec{DisplayName: tc.orgName, Personal: true},
			}, metav1.CreateOptions{}); err != nil {
				t.Fatalf("seeding org: %v", err)
			}
			// A co-member of a workspace in the static user's personal Org,
			// also in an unrelated Org.
			if _, err := p.railgridClient.UserMembershipIndices().Create(ctx, &tenancyv1alpha1.UserMembershipIndex{
				ObjectMeta: metav1.ObjectMeta{Name: "alice"},
				Spec: tenancyv1alpha1.UserMembershipIndexSpec{Entries: []tenancyv1alpha1.MembershipIndexEntry{
					{OrgUUID: "org-personal", OrgDisplayName: tc.orgName, Role: "member"},
					{OrgUUID: "org-personal", OrgDisplayName: tc.orgName, WorkspaceUUID: "ws-1", Role: "member"},
					{OrgUUID: "org-other", OrgDisplayName: "Other", Role: "admin"},
				}},
			}, metav1.CreateOptions{}); err != nil {
				t.Fatalf("seeding index: %v", err)
			}

			if err := p.ScrubStaticTokenUsers(ctx); err != nil {
				t.Fatalf("ScrubStaticTokenUsers: %v", err)
			}

			assertShowsOnlyRBACIdentity(t, getStaticTokenUser(t, p, testStaticToken), testStaticToken)
			org, err := p.railgridClient.Organizations().Get(ctx, "org-personal", metav1.GetOptions{})
			if err != nil {
				t.Fatalf("getting org: %v", err)
			}
			if org.Spec.DisplayName != tc.wantName {
				t.Errorf("org displayName = %q, want %q", org.Spec.DisplayName, tc.wantName)
			}
			idx, err := p.railgridClient.UserMembershipIndices().Get(ctx, "alice", metav1.GetOptions{})
			if err != nil {
				t.Fatalf("getting index: %v", err)
			}
			for _, e := range idx.Spec.Entries {
				want := tc.wantName
				if e.OrgUUID == "org-other" {
					want = "Other"
				}
				if e.OrgDisplayName != want {
					t.Errorf("index row %s/%s orgDisplayName = %q, want %q", e.OrgUUID, e.WorkspaceUUID, e.OrgDisplayName, want)
				}
			}
		})
	}
}
