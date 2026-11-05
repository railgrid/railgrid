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

package identity

import (
	"context"
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"

	tenancyv1alpha1 "github.com/railgrid/railgrid/apis/tenancy/v1alpha1"
	railgridclient "github.com/railgrid/railgrid/pkg/client"
)

const (
	testIssuer = "https://auth.example.com"
	testSub    = "github-sub"
)

// The hash is baked into running hubs as every OIDC User's sub label and
// linked-identity label names; changing it orphans every account.
func TestOIDCSubjectHashIsStable(t *testing.T) {
	const want = "6f1ee006b40df424e686f7c0c40374428ff4028f0e5526e09f3ffaf582d50a3"
	got := OIDCSubjectHash(testIssuer, testSub)
	if got != want {
		t.Fatalf("OIDCSubjectHash(%s, %s) = %q, want %q", testIssuer, testSub, got, want)
	}
	if len(got) != 63 {
		t.Fatalf("hash length = %d, want 63 (label value and name limit)", len(got))
	}
	if OIDCSubjectHash("https://other.example", testSub) == got {
		t.Fatal("the same subject under two issuers must be two identities")
	}
	label := LinkedIdentityLabel(got)
	if !strings.HasPrefix(label, LinkedIdentityLabelPrefix) || strings.TrimPrefix(label, LinkedIdentityLabelPrefix) != got {
		t.Fatalf("LinkedIdentityLabel = %q", label)
	}
}

func TestHasOIDCIdentity(t *testing.T) {
	hash := OIDCSubjectHash(testIssuer, testSub)
	for name, tt := range map[string]struct {
		labels map[string]string
		want   bool
	}{
		"no labels":       {nil, false},
		"unrelated label": {map[string]string{"tenants.railgrid.ai/invited": "true"}, false},
		"primary":         {map[string]string{LabelSubject: hash}, true},
		"linked only":     {map[string]string{LinkedIdentityLabel(hash): "true"}, true},
	} {
		user := &tenancyv1alpha1.User{ObjectMeta: metav1.ObjectMeta{Labels: tt.labels}}
		if got := HasOIDCIdentity(user); got != tt.want {
			t.Errorf("%s: HasOIDCIdentity = %v, want %v", name, got, tt.want)
		}
	}
}

func TestFindUserByOIDCSubject(t *testing.T) {
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{
		railgridclient.UserGVR: "UserList",
	})
	client := railgridclient.NewFromDynamic(dyn)
	ctx := context.Background()
	primary, linked := OIDCSubjectHash(testIssuer, testSub), OIDCSubjectHash(testIssuer, "google-sub")
	if _, err := client.Users().Create(ctx, &tenancyv1alpha1.User{ObjectMeta: metav1.ObjectMeta{
		Name:   "user-alice",
		Labels: map[string]string{LabelSubject: primary, LinkedIdentityLabel(linked): "true"},
	}}, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	for _, hash := range []string{primary, linked} {
		user, err := FindUserByOIDCSubject(ctx, client, hash)
		if err != nil {
			t.Fatal(err)
		}
		if user == nil || user.Name != "user-alice" {
			t.Fatalf("FindUserByOIDCSubject(%s) = %v, want user-alice", hash, user)
		}
	}
	user, err := FindUserByOIDCSubject(ctx, client, OIDCSubjectHash(testIssuer, "unknown"))
	if err != nil || user != nil {
		t.Fatalf("unknown identity: got %v, %v; want nil, nil", user, err)
	}
}

func TestIdentityEntry(t *testing.T) {
	user := &tenancyv1alpha1.User{Spec: tenancyv1alpha1.UserSpec{OIDCProviders: []tenancyv1alpha1.OIDCProvider{
		{Name: "dex", ProviderID: "legacy-sub", Email: "legacy@example.com"},
		{Name: "dex", Issuer: testIssuer, ProviderID: testSub, Email: "alice@example.com"},
	}}}
	if e := IdentityEntry(user, testIssuer, testSub); e == nil || e.Email != "alice@example.com" {
		t.Fatalf("issuer and subject: got %+v", e)
	}
	if e := IdentityEntry(user, testIssuer, "legacy-sub"); e == nil || e.Email != "legacy@example.com" {
		t.Fatalf("legacy entry without issuer: got %+v", e)
	}
	if e := IdentityEntry(user, "https://other.example", testSub); e != nil {
		t.Fatalf("other issuer: got %+v, want nil", e)
	}
	if e := IdentityEntry(user, testIssuer, "nobody"); e != nil {
		t.Fatalf("unknown subject: got %+v, want nil", e)
	}

	// The same subject bound under a legacy entry and under a named issuer
	// (Dex moved hostname, same connector): the exact entry wins.
	user.Spec.OIDCProviders = append(user.Spec.OIDCProviders, tenancyv1alpha1.OIDCProvider{Name: "dex", Issuer: "https://new.example", ProviderID: "legacy-sub", Email: "new@example.com"})
	if e := IdentityEntry(user, "https://new.example", "legacy-sub"); e == nil || e.Email != "new@example.com" {
		t.Fatalf("exact issuer over legacy entry: got %+v", e)
	}
	if e := IdentityEntry(user, testIssuer, "legacy-sub"); e == nil || e.Email != "legacy@example.com" {
		t.Fatalf("legacy entry still matches its own issuer: got %+v", e)
	}
}

func TestEmailLinkable(t *testing.T) {
	ptr := func(b bool) *bool { return &b }
	entry := func(email string, verified *bool) tenancyv1alpha1.OIDCProvider {
		return tenancyv1alpha1.OIDCProvider{Name: "dex", Issuer: testIssuer, ProviderID: testSub, Email: email, EmailVerified: verified}
	}
	for name, tt := range map[string]struct {
		entries []tenancyv1alpha1.OIDCProvider
		want    bool
	}{
		"no identities":                {nil, false},
		"legacy entry, no record":      {[]tenancyv1alpha1.OIDCProvider{entry("alice@example.com", nil)}, true},
		"verified":                     {[]tenancyv1alpha1.OIDCProvider{entry("alice@example.com", ptr(true))}, true},
		"unverified":                   {[]tenancyv1alpha1.OIDCProvider{entry("alice@example.com", ptr(false))}, false},
		"case differs":                 {[]tenancyv1alpha1.OIDCProvider{entry("Alice@Example.com", ptr(true))}, true},
		"other email only":             {[]tenancyv1alpha1.OIDCProvider{entry("bob@example.com", ptr(true))}, false},
		"one unverified, one verified": {[]tenancyv1alpha1.OIDCProvider{entry("alice@example.com", ptr(false)), entry("alice@example.com", ptr(true))}, true},
	} {
		user := &tenancyv1alpha1.User{Spec: tenancyv1alpha1.UserSpec{OIDCProviders: tt.entries}}
		if got := EmailLinkable(user, "alice@example.com"); got != tt.want {
			t.Errorf("%s: EmailLinkable = %v, want %v", name, got, tt.want)
		}
	}
}
