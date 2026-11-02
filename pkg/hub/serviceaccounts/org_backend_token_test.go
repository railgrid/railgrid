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

package serviceaccounts

import (
	"context"
	"encoding/base64"
	"strings"
	"testing"
	"time"
)

func orgBackendTestClaims() OrgBackendClaims {
	return OrgBackendClaims{
		User: "alice@example.com", UserUID: "user-uid", OrgUUID: "org", WorkspaceUUID: "workspace",
		ClusterID: "cluster-id", Provider: "planner", ProviderCluster: "provider-cluster",
		ExportPath: "root:railgrid:org-providers:org:planner", ExportName: "planner.providers.railgrid.ai",
		BindingUID: "binding-uid",
	}
}

func TestOrgBackendTokenRoundTripAndExpiry(t *testing.T) {
	now := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	token, expiresAt, err := MintOrgBackendToken(context.Background(), testProofKeySource, orgBackendTestClaims(), now)
	if err != nil {
		t.Fatalf("MintOrgBackendToken: %v", err)
	}
	if !IsOrgBackendToken(token) || !strings.HasPrefix(token, orgBackendTokenPrefix) {
		t.Fatalf("token %q did not have the reserved BYO prefix", token)
	}
	if !expiresAt.Equal(now.Add(orgBackendTokenTTL)) {
		t.Fatalf("expiry %v, want %v", expiresAt, now.Add(orgBackendTokenTTL))
	}
	got, err := VerifyOrgBackendToken(context.Background(), testProofKeySource, token, now.Add(time.Minute))
	if err != nil {
		t.Fatalf("VerifyOrgBackendToken: %v", err)
	}
	want := orgBackendTestClaims()
	want.IssuedAt = now.Unix()
	want.ExpiresAt = now.Add(orgBackendTokenTTL).Unix()
	if got != want {
		t.Fatalf("claims = %+v, want %+v", got, want)
	}
	if _, err := VerifyOrgBackendToken(context.Background(), testProofKeySource, token, expiresAt); err == nil {
		t.Fatal("accepted an expired token")
	}
}

func TestOrgBackendTokenRejectsTamperingAndInvalidInputs(t *testing.T) {
	now := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	token, _, err := MintOrgBackendToken(context.Background(), testProofKeySource, orgBackendTestClaims(), now)
	if err != nil {
		t.Fatal(err)
	}
	wrongKey := StaticProofKeySource(strings.Repeat("x", delegatedProofKeyLength))
	if _, err := VerifyOrgBackendToken(context.Background(), wrongKey, token, now); err == nil {
		t.Fatal("accepted a token signed by another key")
	}
	body, signature, ok := strings.Cut(strings.TrimPrefix(token, orgBackendTokenPrefix), ".")
	if !ok {
		t.Fatal("minted token has no signature separator")
	}
	payload, err := base64.RawURLEncoding.DecodeString(body)
	if err != nil {
		t.Fatal(err)
	}
	position := strings.Index(string(payload), "alice")
	if position < 0 {
		t.Fatal("minted payload does not contain user claim")
	}
	payload[position] = 'b'
	tampered := orgBackendTokenPrefix + base64.RawURLEncoding.EncodeToString(payload) + "." + signature
	for name, mutate := range map[string]func(string) string{
		"payload tampering": func(string) string { return tampered },
		"wrong prefix":      func(token string) string { return "legacy." + strings.TrimPrefix(token, orgBackendTokenPrefix) },
		"oversize":          func(token string) string { return token + strings.Repeat("a", orgBackendTokenMaxBytes) },
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := VerifyOrgBackendToken(context.Background(), testProofKeySource, mutate(token), now); err == nil {
				t.Fatal("accepted invalid token")
			}
		})
	}
	if _, _, err := MintOrgBackendToken(context.Background(), nil, orgBackendTestClaims(), now); err == nil {
		t.Fatal("minted token without a key source")
	}
	for name, change := range map[string]func(*OrgBackendClaims){
		"missing provider":      func(c *OrgBackendClaims) { c.Provider = "" },
		"oversized export path": func(c *OrgBackendClaims) { c.ExportPath = strings.Repeat("x", 513) },
		"control character":     func(c *OrgBackendClaims) { c.User = "alice\nadmin" },
	} {
		t.Run(name, func(t *testing.T) {
			claims := orgBackendTestClaims()
			change(&claims)
			if _, _, err := MintOrgBackendToken(context.Background(), testProofKeySource, claims, now); err == nil {
				t.Fatal("minted token with invalid claims")
			}
		})
	}
}
