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

package mcpaggregate

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	authnv1 "k8s.io/api/authentication/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes"
	kubefake "k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/rest"
	clienttesting "k8s.io/client-go/testing"

	tenancyv1alpha1 "github.com/faroshq/faros/apis/tenancy/v1alpha1"
)

// saToken -> (cluster it is valid in, username kube reports).
type fakeToken struct {
	cluster  string
	username string
}

// fakeKCP fakes TokenReview per tenant cluster: a token authenticates only
// in the cluster it was issued for, mirroring kcp's per-logical-cluster
// ServiceAccount validation. reviews counts TokenReview calls.
type fakeKCP struct {
	tokens  map[string]fakeToken
	reviews int
}

func (f *fakeKCP) clusterConfig(cluster string) *rest.Config {
	return &rest.Config{Host: "https://kcp.test/clusters/" + cluster}
}

func (f *fakeKCP) newClient(cfg *rest.Config) (kubernetes.Interface, error) {
	cluster := strings.TrimPrefix(cfg.Host, "https://kcp.test/clusters/")
	cs := kubefake.NewClientset()
	cs.PrependReactor("create", "tokenreviews", func(action clienttesting.Action) (bool, runtime.Object, error) {
		f.reviews++
		review := action.(clienttesting.CreateAction).GetObject().(*authnv1.TokenReview)
		out := &authnv1.TokenReview{}
		if tok, ok := f.tokens[review.Spec.Token]; ok && tok.cluster == cluster {
			out.Status = authnv1.TokenReviewStatus{Authenticated: true, User: authnv1.UserInfo{Username: tok.username}}
		} else {
			out.Status = authnv1.TokenReviewStatus{Authenticated: false, Error: "invalid bearer token"}
		}
		return true, out, nil
	})
	return cs, nil
}

func newTestVerifier(t *testing.T, opts ...VerifierOption) (*Verifier, *fakeKCP) {
	t.Helper()
	kcp := &fakeKCP{tokens: map[string]fakeToken{
		"sa-tenant-a":    {cluster: "tenant-a", username: ServiceAccountUsername("default")},
		"sa-other-a":     {cluster: "tenant-a", username: ServiceAccountUsername("other")},
		"workload-a":     {cluster: "tenant-a", username: "system:serviceaccount:default:wl-abc"},
		"sa-tenant-b":    {cluster: "tenant-b", username: ServiceAccountUsername("default")},
		"user-shaped-sa": {cluster: "tenant-a", username: "alice"},
	}}
	all := append([]VerifierOption{WithKubeClientFactory(kcp.newClient)}, opts...)
	return NewVerifier(kcp.clusterConfig, all...), kcp
}

func request(t *testing.T, bearer string) *http.Request {
	t.Helper()
	r := httptest.NewRequest(http.MethodPost, testMCPPath, nil)
	r.Header.Set("Authorization", "Bearer "+bearer)
	return r
}

func TestVerifierServiceAccountToken(t *testing.T) {
	cases := []struct {
		name    string
		token   string
		cluster string
		mcpName string
		wantErr error // nil = allow
	}{
		{name: "own SA in its cluster", token: "sa-tenant-a", cluster: "tenant-a", mcpName: "default"},
		{name: "garbage bearer", token: "nope", cluster: "tenant-a", mcpName: "default", wantErr: ErrUnauthenticated},
		{name: "token from another tenant cluster", token: "sa-tenant-b", cluster: "tenant-a", mcpName: "default", wantErr: ErrUnauthenticated},
		{name: "another MCPServer's SA", token: "sa-other-a", cluster: "tenant-a", mcpName: "default", wantErr: ErrForbidden},
		{name: "workload identity SA", token: "workload-a", cluster: "tenant-a", mcpName: "default", wantErr: ErrForbidden},
		{name: "non-SA identity from TokenReview", token: "user-shaped-sa", cluster: "tenant-a", mcpName: "default", wantErr: ErrForbidden},
		{name: "empty bearer", token: "", cluster: "tenant-a", mcpName: "default", wantErr: ErrUnauthenticated},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			v, kcp := newTestVerifier(t)
			err := v.Verify(request(t, tc.token), tc.token, tc.cluster, tc.mcpName)
			if tc.wantErr == nil && err != nil {
				t.Fatalf("Verify() = %v, want nil", err)
			}
			if tc.wantErr != nil && !errors.Is(err, tc.wantErr) {
				t.Fatalf("Verify() = %v, want %v", err, tc.wantErr)
			}
			if tc.token != "" && kcp.reviews != 1 {
				t.Fatalf("TokenReview calls = %d, want 1", kcp.reviews)
			}
		})
	}
}

// TestVerifierFailsClosedWithoutClusterConfig: no kcp config means no
// verification, never a pass-through.
func TestVerifierFailsClosedWithoutClusterConfig(t *testing.T) {
	v := NewVerifier(func(string) *rest.Config { return nil })
	err := v.Verify(request(t, "sa-tenant-a"), "sa-tenant-a", "tenant-a", "default")
	if err == nil || errors.Is(err, ErrUnauthenticated) || errors.Is(err, ErrForbidden) {
		t.Fatalf("Verify() = %v, want an infrastructure error", err)
	}
}

func membershipIndex(entries ...tenancyv1alpha1.MembershipIndexEntry) *tenancyv1alpha1.UserMembershipIndex {
	return &tenancyv1alpha1.UserMembershipIndex{Spec: tenancyv1alpha1.UserMembershipIndexSpec{Entries: entries}}
}

func TestVerifierHubUserMembership(t *testing.T) {
	const (
		org  = "11111111-1111-1111-1111-111111111111"
		ws   = "22222222-2222-2222-2222-222222222222"
		ws2  = "33333333-3333-3333-3333-333333333333"
		gone = "44444444-4444-4444-4444-444444444444"
	)
	deleted := metav1.Now()
	idx := membershipIndex(
		tenancyv1alpha1.MembershipIndexEntry{OrgUUID: org, WorkspaceUUID: ws, Role: "member"},
		tenancyv1alpha1.MembershipIndexEntry{OrgUUID: gone, Role: "admin", SoftDeletedAt: &deleted},
	)
	orgAdmin := membershipIndex(tenancyv1alpha1.MembershipIndexEntry{OrgUUID: org, Role: "admin"})

	cases := []struct {
		name    string
		cluster string
		index   *tenancyv1alpha1.UserMembershipIndex
		wantErr error
	}{
		{name: "workspace member by path", cluster: tenantPathRoot + org + ":" + ws, index: idx},
		{name: "workspace member by cluster id", cluster: "lc-ws", index: idx},
		{name: "member of a different workspace", cluster: tenantPathRoot + org + ":" + ws2, index: idx, wantErr: ErrForbidden},
		{name: "workspace member addressing the org cluster", cluster: tenantPathRoot + org, index: idx, wantErr: ErrForbidden},
		{name: "org admin covers every workspace", cluster: tenantPathRoot + org + ":" + ws2, index: orgAdmin},
		{name: "org admin covers the org cluster", cluster: tenantPathRoot + org, index: orgAdmin},
		{name: "soft-deleted membership", cluster: tenantPathRoot + gone, index: idx, wantErr: ErrForbidden},
		{name: "cluster outside the tenants tree", cluster: "root:faros:providers:infra", index: orgAdmin, wantErr: ErrForbidden},
		{name: "no index at all", cluster: tenantPathRoot + org, index: nil, wantErr: ErrForbidden},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			v, kcp := newTestVerifier(t, WithClusterPathResolver(func(_ context.Context, cluster string) (string, error) {
				if cluster == "lc-ws" {
					return tenantPathRoot + org + ":" + ws, nil
				}
				return "", errors.New("unknown cluster " + cluster)
			}))
			v.SetUserIdentity(
				func(*http.Request) (string, error) { return "alice", nil },
				func(context.Context, string) (*tenancyv1alpha1.UserMembershipIndex, error) { return tc.index, nil },
			)
			err := v.Verify(request(t, "user-token"), "user-token", tc.cluster, "default")
			if tc.wantErr == nil && err != nil {
				t.Fatalf("Verify() = %v, want nil", err)
			}
			if tc.wantErr != nil && !errors.Is(err, tc.wantErr) {
				t.Fatalf("Verify() = %v, want %v", err, tc.wantErr)
			}
			if kcp.reviews != 0 {
				t.Fatalf("a hub user bearer was sent to TokenReview %d times, want 0", kcp.reviews)
			}
		})
	}
}

// TestVerifierUnknownUserFallsThroughToServiceAccount: when the hub does not
// recognise the bearer as a user, the SA path decides — so SA tokens keep
// working with the user branch enabled, and a stranger is still 401.
func TestVerifierUnknownUserFallsThroughToServiceAccount(t *testing.T) {
	v, kcp := newTestVerifier(t)
	v.SetUserIdentity(
		func(*http.Request) (string, error) { return "", errors.New("not a hub user token") },
		func(context.Context, string) (*tenancyv1alpha1.UserMembershipIndex, error) {
			t.Fatal("membership must not be consulted for an unidentified bearer")
			return nil, nil
		},
	)
	if err := v.Verify(request(t, "sa-tenant-a"), "sa-tenant-a", "tenant-a", "default"); err != nil {
		t.Fatalf("SA token with user branch enabled: %v, want nil", err)
	}
	if err := v.Verify(request(t, "junk"), "junk", "tenant-a", "default"); !errors.Is(err, ErrUnauthenticated) {
		t.Fatalf("junk with user branch enabled: %v, want ErrUnauthenticated", err)
	}
	if kcp.reviews != 2 {
		t.Fatalf("TokenReview calls = %d, want 2", kcp.reviews)
	}

	// An SA-shaped identity from the hub's user path is never treated as a
	// member; it must pass the online SA check instead.
	v.SetUserIdentity(
		func(*http.Request) (string, error) { return "system:serviceaccount:default:other-mcp", nil },
		func(context.Context, string) (*tenancyv1alpha1.UserMembershipIndex, error) {
			t.Fatal("membership must not be consulted for an SA identity")
			return nil, nil
		},
	)
	if err := v.Verify(request(t, "sa-other-a"), "sa-other-a", "tenant-a", "default"); !errors.Is(err, ErrForbidden) {
		t.Fatalf("SA-shaped user identity: %v, want ErrForbidden", err)
	}
}

// TestVerifierCachesClusterPath: the cluster-ID to path lookup is reused.
func TestVerifierCachesClusterPath(t *testing.T) {
	const org = "11111111-1111-1111-1111-111111111111"
	lookups := 0
	v, _ := newTestVerifier(t, WithClusterPathResolver(func(context.Context, string) (string, error) {
		lookups++
		return tenantPathRoot + org, nil
	}))
	v.SetUserIdentity(
		func(*http.Request) (string, error) { return "alice", nil },
		func(context.Context, string) (*tenancyv1alpha1.UserMembershipIndex, error) {
			return membershipIndex(tenancyv1alpha1.MembershipIndexEntry{OrgUUID: org, Role: "member"}), nil
		},
	)
	for i := 0; i < 3; i++ {
		if err := v.Verify(request(t, "u"), "u", "lc-org", "default"); err != nil {
			t.Fatalf("Verify #%d: %v", i, err)
		}
	}
	if lookups != 1 {
		t.Fatalf("cluster path lookups = %d, want 1", lookups)
	}
}

func TestTenantFromPath(t *testing.T) {
	cases := []struct {
		path    string
		org, ws string
		ok      bool
	}{
		{path: "root:faros:tenants:org1", org: "org1", ok: true},
		{path: "root:faros:tenants:org1:ws1", org: "org1", ws: "ws1", ok: true},
		{path: "root:faros:tenants:org1:ws1:edge"},
		{path: "root:faros:tenants:"},
		{path: "root:faros:tenants:org1:"},
		{path: "root:faros:providers:infra"},
		{path: "root"},
		{path: ""},
	}
	for _, tc := range cases {
		org, ws, ok := tenantFromPath(tc.path)
		if org != tc.org || ws != tc.ws || ok != tc.ok {
			t.Errorf("tenantFromPath(%q) = (%q, %q, %v), want (%q, %q, %v)", tc.path, org, ws, ok, tc.org, tc.ws, tc.ok)
		}
	}
}

func TestServiceAccountNaming(t *testing.T) {
	if got := ServiceAccountUsername("default"); got != "system:serviceaccount:default:default-mcp" {
		t.Fatalf("ServiceAccountUsername = %q", got)
	}
}
