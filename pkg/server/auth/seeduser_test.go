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

package auth

import (
	"context"
	"fmt"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	clienttesting "k8s.io/client-go/testing"
	"k8s.io/klog/v2"

	tenancyv1alpha1 "github.com/railgrid/railgrid/apis/tenancy/v1alpha1"
	railgridclient "github.com/railgrid/railgrid/pkg/client"
	"github.com/railgrid/railgrid/pkg/util/identity"
)

const (
	testIssuer      = "https://auth.example.com"
	otherIssuer     = "https://idp.example.org"
	githubSub       = "github-sub"
	googleSub       = "google-sub"
	aliceEmail      = "alice@example.com"
	aliceUserName   = "user-alice"
	invitedUserName = "user-invited"
)

func newSeedTestHandler(t *testing.T, linkIssuers []string, users ...*tenancyv1alpha1.User) (*Handler, *railgridclient.Client) {
	t.Helper()
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{
		railgridclient.UserGVR: "UserList",
	})
	// The object tracker rejects nameless objects; emulate generateName.
	created := 0
	dyn.PrependReactor("create", "users", func(action clienttesting.Action) (bool, runtime.Object, error) {
		obj := action.(clienttesting.CreateAction).GetObject().(*unstructured.Unstructured)
		if obj.GetName() == "" {
			created++
			obj.SetName(fmt.Sprintf("%s%d", obj.GetGenerateName(), created))
		}
		return false, nil, nil
	})
	client := railgridclient.NewFromDynamic(dyn)
	for _, u := range users {
		if _, err := client.Users().Create(context.Background(), u, metav1.CreateOptions{}); err != nil {
			t.Fatalf("seeding user %s: %v", u.Name, err)
		}
	}
	return &Handler{
		railgridClient: client,
		oidcConfig:     &OIDCConfig{IssuerURL: testIssuer, LinkVerifiedEmailIssuers: linkIssuers},
		logger:         klog.Background(),
	}, client
}

// boundAlice is an account created by a GitHub login through Dex.
func boundAlice() *tenancyv1alpha1.User {
	return &tenancyv1alpha1.User{
		ObjectMeta: metav1.ObjectMeta{
			Name:   aliceUserName,
			Labels: map[string]string{identity.LabelSubject: identity.OIDCSubjectHash(testIssuer, githubSub)},
		},
		Spec: tenancyv1alpha1.UserSpec{
			Email:         aliceEmail,
			Name:          "Alice",
			RBACIdentity:  "railgrid:" + aliceEmail,
			OIDCProviders: []tenancyv1alpha1.OIDCProvider{{Name: "dex", ProviderID: githubSub, Email: aliceEmail}},
		},
	}
}

func alice(issuer, sub string, verified bool) oidcIdentity {
	return oidcIdentity{Issuer: issuer, Subject: sub, Email: aliceEmail, EmailVerified: verified, Name: "Alice"}
}

func userCount(t *testing.T, client *railgridclient.Client) int {
	t.Helper()
	list, err := client.Users().List(context.Background(), metav1.ListOptions{})
	if err != nil {
		t.Fatalf("listing users: %v", err)
	}
	return len(list.Items)
}

func TestSeedUserKnownSubjectReturnsExistingUser(t *testing.T) {
	h, client := newSeedTestHandler(t, nil, boundAlice())

	got, err := h.seedUser(context.Background(), alice(testIssuer, githubSub, true))
	if err != nil {
		t.Fatal(err)
	}
	if got != aliceUserName {
		t.Fatalf("seedUser = %q, want %q", got, aliceUserName)
	}
	if n := userCount(t, client); n != 1 {
		t.Fatalf("got %d users, want 1", n)
	}
}

func TestSeedUserNewSubjectWithoutLinkingCreatesUser(t *testing.T) {
	h, client := newSeedTestHandler(t, nil, boundAlice())

	got, err := h.seedUser(context.Background(), alice(testIssuer, googleSub, true))
	if err != nil {
		t.Fatal(err)
	}
	if got == aliceUserName {
		t.Fatal("linking is disabled, but the new subject was bound to the existing user")
	}
	if n := userCount(t, client); n != 2 {
		t.Fatalf("got %d users, want 2", n)
	}
	user, err := client.Users().Get(context.Background(), got, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if p := user.Spec.OIDCProviders; len(p) != 1 || p[0].Issuer != testIssuer || p[0].ProviderID != googleSub {
		t.Fatalf("new user providers = %+v, want one entry for %s/%s", p, testIssuer, googleSub)
	}
}

func TestSeedUserLinksVerifiedEmailFromAllowedIssuer(t *testing.T) {
	// Trailing slash on the allow-list entry must not matter.
	h, client := newSeedTestHandler(t, []string{testIssuer + "/"}, boundAlice())
	ctx := context.Background()

	got, err := h.seedUser(ctx, alice(testIssuer, googleSub, true))
	if err != nil {
		t.Fatal(err)
	}
	if got != aliceUserName {
		t.Fatalf("seedUser = %q, want the existing user %q", got, aliceUserName)
	}
	if n := userCount(t, client); n != 1 {
		t.Fatalf("got %d users, want 1", n)
	}

	user, err := client.Users().Get(ctx, aliceUserName, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if user.Labels[identity.LinkedIdentityLabel(identity.OIDCSubjectHash(testIssuer, googleSub))] != "true" {
		t.Fatalf("linked identity label missing: %v", user.Labels)
	}
	if user.Labels[identity.LabelSubject] != identity.OIDCSubjectHash(testIssuer, githubSub) {
		t.Fatal("primary identity label changed")
	}
	if p := user.Spec.OIDCProviders; len(p) != 2 || p[1].Issuer != testIssuer || p[1].ProviderID != googleSub {
		t.Fatalf("providers = %+v, want the linked identity appended", p)
	}

	// Both identities now resolve to the account, without linking again.
	for _, sub := range []string{githubSub, googleSub} {
		u, err := identity.FindUserByOIDCSubject(ctx, client, identity.OIDCSubjectHash(testIssuer, sub))
		if err != nil {
			t.Fatal(err)
		}
		if u == nil || u.Name != aliceUserName {
			t.Fatalf("FindUserByOIDCSubject(%s) = %v, want %s", sub, u, aliceUserName)
		}
	}
	if _, err := h.seedUser(ctx, alice(testIssuer, googleSub, true)); err != nil {
		t.Fatal(err)
	}
	user, _ = client.Users().Get(ctx, aliceUserName, metav1.GetOptions{})
	if len(user.Spec.OIDCProviders) != 2 {
		t.Fatalf("second login appended another provider: %+v", user.Spec.OIDCProviders)
	}
}

func TestSeedUserLinksAcrossIssuers(t *testing.T) {
	// The IdP itself changed: the hub now trusts otherIssuer.
	h, client := newSeedTestHandler(t, []string{otherIssuer}, boundAlice())
	h.oidcConfig.IssuerURL = otherIssuer

	got, err := h.seedUser(context.Background(), alice(otherIssuer, "auth0|123", true))
	if err != nil {
		t.Fatal(err)
	}
	if got != aliceUserName {
		t.Fatalf("seedUser = %q, want %q", got, aliceUserName)
	}
	if n := userCount(t, client); n != 1 {
		t.Fatalf("got %d users, want 1", n)
	}
}

func TestSeedUserDoesNotLink(t *testing.T) {
	twin := boundAlice()
	twin.Name = "user-alice-twin"
	twin.Labels = map[string]string{identity.LabelSubject: identity.OIDCSubjectHash(testIssuer, "other-sub")}

	tests := []struct {
		name        string
		linkIssuers []string
		users       []*tenancyv1alpha1.User
		id          oidcIdentity
	}{
		{
			name:        "email not verified",
			linkIssuers: []string{testIssuer},
			users:       []*tenancyv1alpha1.User{boundAlice()},
			id:          alice(testIssuer, googleSub, false),
		},
		{
			name:        "issuer not allowed",
			linkIssuers: []string{otherIssuer},
			users:       []*tenancyv1alpha1.User{boundAlice()},
			id:          alice(testIssuer, googleSub, true),
		},
		{
			name:        "several users share the email",
			linkIssuers: []string{testIssuer},
			users:       []*tenancyv1alpha1.User{boundAlice(), twin},
			id:          alice(testIssuer, googleSub, true),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h, client := newSeedTestHandler(t, tt.linkIssuers, tt.users...)
			got, err := h.seedUser(context.Background(), tt.id)
			if err != nil {
				t.Fatal(err)
			}
			for _, u := range tt.users {
				if got == u.Name {
					t.Fatalf("identity was linked to %s", u.Name)
				}
			}
			if n := userCount(t, client); n != len(tt.users)+1 {
				t.Fatalf("got %d users, want %d", n, len(tt.users)+1)
			}
		})
	}
}

func TestSeedUserAdoptsInvitedUserOnlyWhenUnbound(t *testing.T) {
	invited := &tenancyv1alpha1.User{
		ObjectMeta: metav1.ObjectMeta{
			Name:   invitedUserName,
			Labels: map[string]string{"tenants.railgrid.ai/invited": "true"},
		},
		Spec: tenancyv1alpha1.UserSpec{Email: aliceEmail, Name: "alice", RBACIdentity: "railgrid:" + aliceEmail},
	}
	h, client := newSeedTestHandler(t, nil, invited)
	ctx := context.Background()

	got, err := h.seedUser(ctx, alice(testIssuer, githubSub, true))
	if err != nil {
		t.Fatal(err)
	}
	if got != invitedUserName {
		t.Fatalf("seedUser = %q, want the invited user %q", got, invitedUserName)
	}
	user, _ := client.Users().Get(ctx, invitedUserName, metav1.GetOptions{})
	if user.Labels[identity.LabelSubject] == "" || user.Labels["tenants.railgrid.ai/invited"] != "" {
		t.Fatalf("adopted user labels = %v", user.Labels)
	}

	// Now bound: a different subject with the same email must not take it
	// over while linking is off.
	got, err = h.seedUser(ctx, alice(testIssuer, googleSub, true))
	if err != nil {
		t.Fatal(err)
	}
	if got == invitedUserName {
		t.Fatal("a bound user was adopted by email")
	}
}
