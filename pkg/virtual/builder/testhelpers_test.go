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

package builder

import (
	"encoding/base64"
	"encoding/json"
	"testing"
)

// buildFakeJWT creates a minimal three-part JWT with the given payload bytes.
// The signature part is a dummy — signature verification is not performed by
// the unit under test (kcp handles that on the real path).
func buildFakeJWT(t *testing.T, payload []byte) string {
	t.Helper()
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"RS256","typ":"JWT"}`))
	body := base64.RawURLEncoding.EncodeToString(payload)
	sig := base64.RawURLEncoding.EncodeToString([]byte("fake-signature"))
	return header + "." + body + "." + sig
}

// buildFakeOIDCToken builds a JWT that looks like an OIDC token (no kcp SA
// claims), so parseServiceAccountToken returns false for it.
func buildFakeOIDCToken(t *testing.T) string {
	t.Helper()
	payload, err := json.Marshal(map[string]interface{}{
		"iss":   "https://dex.example.com",
		"sub":   "user@example.com",
		"email": "user@example.com",
	})
	if err != nil {
		t.Fatalf("marshal OIDC payload: %v", err)
	}
	return buildFakeJWT(t, payload)
}

// buildFakeSAToken builds a JWT that parseServiceAccountToken recognises as a
// kcp ServiceAccount token with the given cluster name embedded in its claims.
func buildFakeSAToken(t *testing.T, clusterName string) string {
	t.Helper()
	payload, err := json.Marshal(map[string]interface{}{
		"iss": "kubernetes/serviceaccount",
		"kubernetes.io/serviceaccount/clusterName": clusterName,
		"sub": "system:serviceaccount:default:my-sa",
	})
	if err != nil {
		t.Fatalf("marshal SA payload: %v", err)
	}
	return buildFakeJWT(t, payload)
}
