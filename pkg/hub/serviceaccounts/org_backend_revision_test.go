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
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestOrgBackendTokenTrustRevisionRoundTripAndValidation(t *testing.T) {
	now := time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC)
	claims := orgBackendTestClaims()
	claims.TrustRevision = "9a7b6c5d"
	token, _, err := MintOrgBackendToken(context.Background(), testProofKeySource, claims, now)
	if err != nil {
		t.Fatalf("MintOrgBackendToken: %v", err)
	}
	got, err := VerifyOrgBackendToken(context.Background(), testProofKeySource, token, now.Add(time.Second))
	if err != nil {
		t.Fatalf("VerifyOrgBackendToken: %v", err)
	}
	if got.TrustRevision != claims.TrustRevision {
		t.Fatalf("trust revision = %q, want %q", got.TrustRevision, claims.TrustRevision)
	}

	for name, revision := range map[string]string{
		"too long":   strings.Repeat("a", 129),
		"slash":      "revision/1",
		"underscore": "revision_1",
	} {
		t.Run(name, func(t *testing.T) {
			invalid := orgBackendTestClaims()
			invalid.TrustRevision = revision
			if _, _, err := MintOrgBackendToken(context.Background(), testProofKeySource, invalid, now); err == nil {
				t.Fatal("minted token with invalid trust revision")
			}
		})
	}
}

func TestOrgBackendTokenSignatureCoversTrustRevision(t *testing.T) {
	now := time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC)
	claims := orgBackendTestClaims()
	claims.TrustRevision = "revision-1"
	token, _, err := MintOrgBackendToken(context.Background(), testProofKeySource, claims, now)
	if err != nil {
		t.Fatal(err)
	}
	body, signature, ok := strings.Cut(strings.TrimPrefix(token, orgBackendTokenPrefix), ".")
	if !ok {
		t.Fatal("minted token has no signature separator")
	}
	payload, err := base64.RawURLEncoding.DecodeString(body)
	if err != nil {
		t.Fatal(err)
	}
	changed := bytes.Replace(payload, []byte(`"trustRevision":"revision-1"`), []byte(`"trustRevision":"revision-2"`), 1)
	if bytes.Equal(changed, payload) {
		t.Fatal("minted token payload did not contain the trust revision")
	}
	tampered := orgBackendTokenPrefix + base64.RawURLEncoding.EncodeToString(changed) + "." + signature
	if _, err := VerifyOrgBackendToken(context.Background(), testProofKeySource, tampered, now); err == nil {
		t.Fatal("accepted a token after its trust revision was changed without resigning")
	}
}

func TestOrgBackendTokenDecodesLegacyPayloadWithoutTrustRevision(t *testing.T) {
	now := time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC)
	claims := orgBackendTestClaims()
	claims.IssuedAt = now.Unix()
	claims.ExpiresAt = now.Add(orgBackendTokenTTL).Unix()
	// This is the original v1 payload layout, before trustRevision was added.
	type legacyClaims struct {
		User            string `json:"user"`
		UserUID         string `json:"userUID"`
		OrgUUID         string `json:"orgUUID"`
		WorkspaceUUID   string `json:"workspaceUUID"`
		ClusterID       string `json:"clusterID"`
		Provider        string `json:"provider"`
		ProviderCluster string `json:"providerCluster"`
		ExportPath      string `json:"exportPath"`
		ExportName      string `json:"exportName"`
		BindingUID      string `json:"bindingUID"`
		IssuedAt        int64  `json:"iat"`
		ExpiresAt       int64  `json:"exp"`
	}
	legacy := legacyClaims{
		User: claims.User, UserUID: claims.UserUID, OrgUUID: claims.OrgUUID,
		WorkspaceUUID: claims.WorkspaceUUID, ClusterID: claims.ClusterID,
		Provider: claims.Provider, ProviderCluster: claims.ProviderCluster,
		ExportPath: claims.ExportPath, ExportName: claims.ExportName,
		BindingUID: claims.BindingUID, IssuedAt: claims.IssuedAt, ExpiresAt: claims.ExpiresAt,
	}
	payload, err := json.Marshal(legacy)
	if err != nil {
		t.Fatal(err)
	}
	key, err := testProofKeySource.DelegatedProofKey(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	token := orgBackendTokenPrefix + base64.RawURLEncoding.EncodeToString(payload) + "." + base64.RawURLEncoding.EncodeToString(orgBackendMAC(key, payload))
	got, err := VerifyOrgBackendToken(context.Background(), testProofKeySource, token, now)
	if err != nil {
		t.Fatalf("legacy token failed to decode: %v", err)
	}
	if got.TrustRevision != "" || got.User != claims.User || got.BindingUID != claims.BindingUID {
		t.Fatalf("legacy claims = %+v", got)
	}
}
