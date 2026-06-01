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

// Cross-workspace SA token isolation. A SA token minted in User A's
// workspace must not authenticate against User B's workspace — the
// token's identity is bound to its workspace, not to "any caller of
// /api".

package cases

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"sigs.k8s.io/e2e-framework/pkg/envconf"
	"sigs.k8s.io/e2e-framework/pkg/features"

	"github.com/faroshq/faros-kedge/test/e2e/framework"
)

// TenancySATokenCrossWorkspace verifies that a SA token issued in
// (orgA, wsA) does not work against (orgB, wsB) belonging to a
// different user. Skipped outside the OIDC suite (we need two
// distinct identities).
func TenancySATokenCrossWorkspace() features.Feature {
	return features.New("Tenancy/SATokenCrossWorkspace").
		Assess("sa_token_from_a_rejected_against_b", func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
			dex := framework.DexEnvFrom(ctx)
			if dex == nil || dex.User2Email == "" {
				t.Skip("requires OIDC suite with second Dex user configured")
			}
			hubURL := tenancyHubURL(ctx, t)

			// ── User A: log in, create org+ws, mint SA token. ─────────
			loginA, cancelA := context.WithTimeout(ctx, 90*time.Second)
			defer cancelA()
			resA, err := framework.HeadlessOIDCLogin(loginA, hubURL, dex.UserEmail, dex.UserPassword)
			if err != nil {
				t.Fatalf("User A login: %v", err)
			}
			orgA, err := framework.CreateOrgViaREST(ctx, hubURL, resA.IDToken, uniqueName("e2e-sa-iso-a"))
			if err != nil {
				t.Fatalf("orgA: %v", err)
			}
			t.Cleanup(func() {
				_, _ = framework.DeleteOrgViaREST(context.Background(), hubURL, resA.IDToken, orgA.UUID)
			})
			wsA, err := framework.CreateWorkspaceViaREST(ctx, hubURL, resA.IDToken, orgA.UUID, "wsA")
			if err != nil {
				t.Fatalf("wsA: %v", err)
			}

			// Create an admin SA in wsA and mint a token.
			code, body, err := framework.DoRESTRequest(ctx, http.MethodPost,
				saListURL(hubURL, orgA.UUID, wsA.UUID), resA.IDToken,
				orgWSHeaders(orgA.UUID, wsA.UUID),
				map[string]string{"displayName": "iso-sa", "role": "admin"})
			if err != nil || code != http.StatusCreated {
				t.Fatalf("create SA in wsA: code=%d err=%v body=%s", code, err, body)
			}
			var sa framework.SAResponse
			_ = json.Unmarshal(body, &sa)

			code, body, _ = framework.DoRESTRequest(ctx, http.MethodPost,
				saTokenURL(hubURL, orgA.UUID, wsA.UUID, sa.UUID), resA.IDToken,
				orgWSHeaders(orgA.UUID, wsA.UUID), nil)
			if code != http.StatusCreated {
				t.Fatalf("mint token: code=%d body=%s", code, body)
			}
			var tok framework.TokenResponse
			_ = json.Unmarshal(body, &tok)
			if tok.Token == "" {
				t.Fatal("issued token is empty")
			}

			// ── User B: log in, create org+ws ─────────────────────────
			loginB, cancelB := context.WithTimeout(ctx, 90*time.Second)
			defer cancelB()
			resB, err := framework.HeadlessOIDCLogin(loginB, hubURL, dex.User2Email, dex.User2Password)
			if err != nil {
				t.Fatalf("User B login: %v", err)
			}
			orgB, err := framework.CreateOrgViaREST(ctx, hubURL, resB.IDToken, uniqueName("e2e-sa-iso-b"))
			if err != nil {
				t.Fatalf("orgB: %v", err)
			}
			t.Cleanup(func() {
				_, _ = framework.DeleteOrgViaREST(context.Background(), hubURL, resB.IDToken, orgB.UUID)
			})
			wsB, err := framework.CreateWorkspaceViaREST(ctx, hubURL, resB.IDToken, orgB.UUID, "wsB")
			if err != nil {
				t.Fatalf("wsB: %v", err)
			}

			// User A's SA token aimed at User B's workspace. Must be
			// rejected — accepting it would be a critical security bug.
			// 500 is allowed because the OIDC-mode auth handler currently
			// maps "kube SA token doesn't validate against Dex keys" to
			// InternalError rather than 401 — the rejection is still
			// real, the status code is just coarse. The invariant we care
			// about is "no 2xx".
			code, body, err = framework.DoRESTRequest(ctx, http.MethodGet,
				workspaceURL(hubURL, orgB.UUID, wsB.UUID), tok.Token,
				orgWSHeaders(orgB.UUID, wsB.UUID), nil)
			if err != nil {
				t.Fatalf("cross-ws GET: %v", err)
			}
			if code >= 200 && code < 300 {
				t.Fatalf("User A SA token against User B workspace: expected rejection, got %d (body=%s)", code, body)
			}
			t.Logf("cross-workspace SA token rejected with %d (correct)", code)
			return ctx
		}).
		Feature()
}
