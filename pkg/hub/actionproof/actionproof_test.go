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

package actionproof

import (
	"context"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/railgrid/provider-sdk/dataplane"

	"github.com/railgrid/railgrid/pkg/hub/serviceaccounts"
)

var testKey = serviceaccounts.StaticProofKeySource(strings.Repeat("k", 32))

func testClaims() Claims {
	return Claims{
		UserID:          "user-123",
		RBACIdentity:    "oidc:alice",
		Groups:          []string{"system:authenticated"},
		Extra:           map[string][]string{"authentication.kcp.io/cluster-name": {"root:railgrid:tenants:org:ws"}},
		OrgUUID:         "org",
		WorkspaceUUID:   "ws",
		ClusterID:       "cluster-id",
		Provider:        "app-studio",
		Group:           "ai.railgrid.ai",
		Version:         "v1alpha1",
		Resource:        "projects",
		ParentName:      "project-1",
		Verb:            "publishing-members",
		Method:          http.MethodGet,
		ProviderOrgUUID: "",
	}
}

func proofRequest(token string) *http.Request {
	r := httptestNewRequest()
	r.Header.Set(Header, token)
	r.Header.Set("X-Railgrid-Org", "org")
	r.Header.Set("X-Railgrid-Workspace", "ws")
	r.Header.Set(dataplane.HeaderTenant, "cluster-id")
	r.Header.Set(dataplane.HeaderCluster, "cluster-id")
	r.Header.Set("Authorization", "Bearer provider-service-account-token")
	return r
}

func TestSignAndVerify(t *testing.T) {
	now := time.Date(2026, time.October, 7, 12, 0, 0, 0, time.UTC)
	token, err := Sign(context.Background(), testKey, testClaims(), now)
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}
	var authenticated, scoped bool
	verifier := &Verifier{
		Keys: testKey,
		Authenticate: func(_ context.Context, r *http.Request, claims Claims) error {
			authenticated = r.Header.Get("Authorization") == "Bearer provider-service-account-token" && claims.Provider == "app-studio"
			return nil
		},
		ValidateScope: func(_ context.Context, claims Claims) error {
			scoped = claims.UserID == "user-123" && claims.ParentName == "project-1"
			return nil
		},
		Now: func() time.Time { return now.Add(time.Second) },
	}
	got, err := verifier.Verify(context.Background(), proofRequest(token))
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if !authenticated || !scoped {
		t.Fatalf("callbacks authenticated=%v scoped=%v, want both true", authenticated, scoped)
	}
	if got.UserID != "user-123" || got.RBACIdentity != "oidc:alice" || got.OrgUUID != "org" || got.WorkspaceUUID != "ws" || got.ClusterID != "cluster-id" || got.Verb != "publishing-members" {
		t.Fatalf("verified claims = %+v", got)
	}
	if got.ProofVersion != proofVersion || got.ExpiresAt-got.IssuedAt != int64(defaultTTL/time.Second) || len(got.Nonce) != 32 {
		t.Fatalf("proof lifetime/version/nonce = %d/%d/%q", got.ProofVersion, got.ExpiresAt-got.IssuedAt, got.Nonce)
	}
}

func TestVerifyRejectsInvalidProofsAndScopes(t *testing.T) {
	now := time.Date(2026, time.October, 7, 12, 0, 0, 0, time.UTC)
	token, err := Sign(context.Background(), testKey, testClaims(), now)
	if err != nil {
		t.Fatal(err)
	}
	verifier := &Verifier{
		Keys:          testKey,
		Authenticate:  func(context.Context, *http.Request, Claims) error { return nil },
		ValidateScope: func(context.Context, Claims) error { return nil },
		Now:           func() time.Time { return now.Add(time.Second) },
	}
	tests := []struct {
		name  string
		setup func(*http.Request, *Verifier)
	}{
		{name: "missing header", setup: func(r *http.Request, _ *Verifier) { r.Header.Del(Header) }},
		{name: "duplicate header", setup: func(r *http.Request, _ *Verifier) { r.Header.Add(Header, r.Header.Get(Header)) }},
		{name: "tampered signature", setup: func(r *http.Request, _ *Verifier) {
			parts := strings.Split(r.Header.Get(Header), ".")
			signature, _ := base64.RawURLEncoding.DecodeString(parts[1])
			signature[0] ^= 0xff
			parts[1] = base64.RawURLEncoding.EncodeToString(signature)
			r.Header.Set(Header, strings.Join(parts, "."))
		}},
		{name: "expired", setup: func(_ *http.Request, v *Verifier) {
			v.Now = func() time.Time { return now.Add(defaultTTL + time.Second) }
		}},
		{name: "tenant header mismatch", setup: func(r *http.Request, _ *Verifier) { r.Header.Set("X-Railgrid-Org", "another-org") }},
		{name: "cluster tenant mismatch", setup: func(r *http.Request, _ *Verifier) { r.Header.Set(dataplane.HeaderTenant, "another-cluster") }},
		{name: "provider authentication failure", setup: func(_ *http.Request, v *Verifier) {
			v.Authenticate = func(context.Context, *http.Request, Claims) error { return errInvalid }
		}},
		{name: "scope validation failure", setup: func(_ *http.Request, v *Verifier) {
			v.ValidateScope = func(context.Context, Claims) error { return errInvalid }
		}},
		{name: "missing live checks", setup: func(_ *http.Request, v *Verifier) { v.ValidateScope = nil }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := proofRequest(token)
			v := *verifier
			tt.setup(r, &v)
			if _, err := v.Verify(context.Background(), r); err == nil {
				t.Fatal("Verify succeeded, want rejection")
			}
		})
	}
}

func TestSignRejectsIncompleteClaimsAndMissingKey(t *testing.T) {
	now := time.Date(2026, time.October, 7, 12, 0, 0, 0, time.UTC)
	if _, err := Sign(context.Background(), testKey, Claims{}, now); err == nil {
		t.Fatal("Sign with incomplete claims succeeded")
	}
	if _, err := Sign(context.Background(), nil, testClaims(), now); err == nil {
		t.Fatal("Sign without a proof key succeeded")
	}
}

func TestInviteUserClaimIsLimitedToAppStudioSharingInvites(t *testing.T) {
	now := time.Date(2026, time.October, 7, 12, 0, 0, 0, time.UTC)
	claims := testClaims()
	claims.Method = http.MethodPost
	claims.Verb = "publishing-grants"
	claims.InviteUser = "alice@example.com"
	if _, err := Sign(context.Background(), testKey, claims, now); err != nil {
		t.Fatalf("Sign valid invite proof: %v", err)
	}

	for name, mutate := range map[string]func(*Claims){
		"untrimmed recipient":    func(c *Claims) { c.InviteUser = " alice@example.com" },
		"overlong recipient":     func(c *Claims) { c.InviteUser = strings.Repeat("a", 321) + "@example.com" },
		"not an email recipient": func(c *Claims) { c.InviteUser = "alice" },
		"read source method":     func(c *Claims) { c.Method = http.MethodGet },
		"other action":           func(c *Claims) { c.Verb = "publishing-members" },
		"other provider":         func(c *Claims) { c.Provider = "other-provider" },
	} {
		t.Run(name, func(t *testing.T) {
			caseClaims := claims
			mutate(&caseClaims)
			if _, err := Sign(context.Background(), testKey, caseClaims, now); err == nil {
				t.Fatal("Sign accepted an invite user outside the App Studio sharing POST contract")
			}
		})
	}
}

func httptestNewRequest() *http.Request {
	return httptest.NewRequest(http.MethodGet, "https://hub.example/api/orgs/org/memberships", nil)
}
