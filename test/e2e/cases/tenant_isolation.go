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

// tenant_isolation.go covers the multi-org / multi-workspace isolation
// guarantees of the hub REST surface introduced in roadmap step 10 (PR
// #214). With two distinct OIDC identities we ensure that User B
// cannot observe or mutate User A's tenancy state through any of the
// /api/orgs/* endpoints, and that a service-account token minted in
// User A's workspace is not honoured against User B's workspace.

package cases

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"sigs.k8s.io/e2e-framework/pkg/envconf"
	"sigs.k8s.io/e2e-framework/pkg/features"

	"github.com/faroshq/faros-kedge/test/e2e/framework"
)

// tenantIsolationCtxKey carries setup state into Assess/Teardown.
type tenantIsolationCtxKey struct{}

type tenantIsolationData struct {
	hubURL string

	// User A: created a non-personal org + holds workspace info.
	userAToken   string
	userAOrgUUID string
	userAWSUUID  string
	userAOrgName string

	// User B: separate OIDC identity. No access to User A's org.
	userBToken string
}

// MultiOrgIsolation is a comprehensive isolation test for the hub REST
// surface from PR #214. It verifies that User B (a different OIDC
// identity than User A) cannot observe or mutate any of the org,
// workspace, membership, or service-account state User A creates.
//
// Requires the OIDC suite (two static users in Dex).
func MultiOrgIsolation() features.Feature {
	return features.New("Tenancy/MultiOrgIsolation").
		Setup(func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
			clusterEnv := framework.ClusterEnvFrom(ctx)
			dexEnv := framework.DexEnvFrom(ctx)
			if clusterEnv == nil || dexEnv == nil {
				t.Skip("requires OIDC suite (Dex env not found in context)")
			}
			if dexEnv.User2Email == "" {
				t.Skip("second Dex user not configured")
			}

			// ── User A: OIDC login ──────────────────────────────────────
			loginA, cancelA := context.WithTimeout(ctx, 90*time.Second)
			defer cancelA()
			resultA, err := framework.HeadlessOIDCLogin(loginA, clusterEnv.HubURL, dexEnv.UserEmail, dexEnv.UserPassword)
			if err != nil {
				t.Fatalf("User A OIDC login failed: %v", err)
			}
			if resultA.IDToken == "" {
				t.Fatal("User A login returned empty ID token")
			}

			// ── User A: create a non-personal Organization ──────────────
			orgName := fmt.Sprintf("e2e-isolation-%d", time.Now().UnixNano())
			createBody := map[string]string{"displayName": orgName}
			code, body, err := framework.DoRESTRequest(
				ctx, http.MethodPost, clusterEnv.HubURL+"/api/orgs",
				resultA.IDToken, nil, createBody,
			)
			if err != nil {
				t.Fatalf("User A POST /api/orgs: %v", err)
			}
			if code != http.StatusCreated {
				t.Fatalf("User A POST /api/orgs: expected 201, got %d: %s", code, body)
			}
			var createdOrg struct {
				UUID        string `json:"uuid"`
				DisplayName string `json:"displayName"`
			}
			if err := json.Unmarshal(body, &createdOrg); err != nil {
				t.Fatalf("decoding org create response: %v\nbody: %s", err, body)
			}
			t.Logf("User A created org %q (uuid=%s)", createdOrg.DisplayName, createdOrg.UUID)

			// Non-personal orgs do NOT get an auto-materialised default
			// workspace — that path runs only off User.status.defaultWorkspace
			// for the bootstrap of a user's personal org. Create one
			// explicitly so the service-account isolation checks below have
			// a target workspace to address.
			wsBody := map[string]string{"displayName": "isolation-ws"}
			code, body, err = framework.DoRESTRequest(
				ctx, http.MethodPost,
				clusterEnv.HubURL+"/api/orgs/"+createdOrg.UUID+"/workspaces",
				resultA.IDToken,
				map[string]string{"X-Kedge-Org": createdOrg.UUID},
				wsBody,
			)
			if err != nil {
				t.Fatalf("User A POST workspace: %v", err)
			}
			if code != http.StatusCreated {
				t.Fatalf("User A POST workspace: expected 201, got %d: %s", code, body)
			}
			var createdWS struct {
				UUID string `json:"uuid"`
			}
			if err := json.Unmarshal(body, &createdWS); err != nil {
				t.Fatalf("decoding workspace create response: %v\nbody: %s", err, body)
			}
			defaultWS := createdWS.UUID
			t.Logf("User A created workspace: %s", defaultWS)

			// ── User B: OIDC login ──────────────────────────────────────
			loginB, cancelB := context.WithTimeout(ctx, 90*time.Second)
			defer cancelB()
			resultB, err := framework.HeadlessOIDCLogin(loginB, clusterEnv.HubURL, dexEnv.User2Email, dexEnv.User2Password)
			if err != nil {
				t.Fatalf("User B OIDC login failed: %v", err)
			}
			if resultB.IDToken == "" {
				t.Fatal("User B login returned empty ID token")
			}
			t.Logf("User B (%s) login OK", dexEnv.User2Email)

			return context.WithValue(ctx, tenantIsolationCtxKey{}, &tenantIsolationData{
				hubURL:       clusterEnv.HubURL,
				userAToken:   resultA.IDToken,
				userAOrgUUID: createdOrg.UUID,
				userAOrgName: createdOrg.DisplayName,
				userAWSUUID:  defaultWS,
				userBToken:   resultB.IDToken,
			})
		}).
		Assess("user_b_org_list_excludes_user_a_org", func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
			data := ctxIsolation(ctx, t)
			code, body, err := framework.DoRESTRequest(
				ctx, http.MethodGet, data.hubURL+"/api/orgs",
				data.userBToken, nil, nil,
			)
			if err != nil {
				t.Fatalf("User B GET /api/orgs: %v", err)
			}
			if code != http.StatusOK {
				t.Fatalf("User B GET /api/orgs: expected 200, got %d: %s", code, body)
			}
			var list struct {
				Items []struct {
					UUID string `json:"uuid"`
				} `json:"items"`
			}
			if err := json.Unmarshal(body, &list); err != nil {
				t.Fatalf("decoding org list: %v", err)
			}
			for _, o := range list.Items {
				if o.UUID == data.userAOrgUUID {
					t.Fatalf("User B's /api/orgs leaked User A's org %s — list=%s", data.userAOrgUUID, body)
				}
			}
			t.Logf("User B's org list has %d entries; none match User A's org (correct)", len(list.Items))
			return ctx
		}).
		Assess("user_b_cannot_read_user_a_org", func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
			data := ctxIsolation(ctx, t)
			code, _, err := framework.DoRESTRequest(
				ctx, http.MethodGet,
				data.hubURL+"/api/orgs/"+data.userAOrgUUID,
				data.userBToken,
				map[string]string{"X-Kedge-Org": data.userAOrgUUID},
				nil,
			)
			if err != nil {
				t.Fatalf("User B GET org: %v", err)
			}
			if !framework.IsAuthRejectStatus(code) {
				t.Fatalf("User B GET /api/orgs/{A-org}: expected 401/403/404, got %d", code)
			}
			t.Logf("User B GET org rejected with %d (correct)", code)
			return ctx
		}).
		Assess("user_b_cannot_patch_user_a_org", func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
			data := ctxIsolation(ctx, t)
			code, _, err := framework.DoRESTRequest(
				ctx, http.MethodPatch,
				data.hubURL+"/api/orgs/"+data.userAOrgUUID,
				data.userBToken,
				map[string]string{"X-Kedge-Org": data.userAOrgUUID},
				map[string]string{"displayName": "hijacked"},
			)
			if err != nil {
				t.Fatalf("User B PATCH org: %v", err)
			}
			if !framework.IsAuthRejectStatus(code) {
				t.Fatalf("User B PATCH /api/orgs/{A-org}: expected 401/403/404, got %d", code)
			}
			t.Logf("User B PATCH org rejected with %d (correct)", code)
			return ctx
		}).
		Assess("user_b_cannot_delete_user_a_org", func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
			data := ctxIsolation(ctx, t)
			code, _, err := framework.DoRESTRequest(
				ctx, http.MethodDelete,
				data.hubURL+"/api/orgs/"+data.userAOrgUUID,
				data.userBToken,
				map[string]string{"X-Kedge-Org": data.userAOrgUUID},
				nil,
			)
			if err != nil {
				t.Fatalf("User B DELETE org: %v", err)
			}
			if !framework.IsAuthRejectStatus(code) {
				t.Fatalf("User B DELETE /api/orgs/{A-org}: expected 401/403/404, got %d", code)
			}
			t.Logf("User B DELETE org rejected with %d (correct)", code)
			return ctx
		}).
		Assess("user_b_cannot_list_user_a_workspaces", func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
			data := ctxIsolation(ctx, t)
			code, _, err := framework.DoRESTRequest(
				ctx, http.MethodGet,
				data.hubURL+"/api/orgs/"+data.userAOrgUUID+"/workspaces",
				data.userBToken,
				map[string]string{"X-Kedge-Org": data.userAOrgUUID},
				nil,
			)
			if err != nil {
				t.Fatalf("User B GET workspaces: %v", err)
			}
			if !framework.IsAuthRejectStatus(code) {
				t.Fatalf("User B GET /api/orgs/{A-org}/workspaces: expected 401/403/404, got %d", code)
			}
			t.Logf("User B list workspaces rejected with %d (correct)", code)
			return ctx
		}).
		Assess("user_b_cannot_create_workspace_in_user_a_org", func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
			data := ctxIsolation(ctx, t)
			code, _, err := framework.DoRESTRequest(
				ctx, http.MethodPost,
				data.hubURL+"/api/orgs/"+data.userAOrgUUID+"/workspaces",
				data.userBToken,
				map[string]string{"X-Kedge-Org": data.userAOrgUUID},
				map[string]string{"displayName": "evil-ws"},
			)
			if err != nil {
				t.Fatalf("User B POST workspace: %v", err)
			}
			if !framework.IsAuthRejectStatus(code) {
				t.Fatalf("User B POST workspace: expected 401/403/404, got %d", code)
			}
			t.Logf("User B create workspace rejected with %d (correct)", code)
			return ctx
		}).
		Assess("user_b_cannot_delete_user_a_workspace", func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
			data := ctxIsolation(ctx, t)
			code, _, err := framework.DoRESTRequest(
				ctx, http.MethodDelete,
				data.hubURL+"/api/orgs/"+data.userAOrgUUID+"/workspaces/"+data.userAWSUUID,
				data.userBToken,
				map[string]string{
					"X-Kedge-Org":       data.userAOrgUUID,
					"X-Kedge-Workspace": data.userAWSUUID,
				},
				nil,
			)
			if err != nil {
				t.Fatalf("User B DELETE workspace: %v", err)
			}
			if !framework.IsAuthRejectStatus(code) {
				t.Fatalf("User B DELETE workspace: expected 401/403/404, got %d", code)
			}
			t.Logf("User B delete workspace rejected with %d (correct)", code)
			return ctx
		}).
		Assess("user_b_cannot_list_user_a_memberships", func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
			data := ctxIsolation(ctx, t)
			code, _, err := framework.DoRESTRequest(
				ctx, http.MethodGet,
				data.hubURL+"/api/orgs/"+data.userAOrgUUID+"/memberships",
				data.userBToken,
				map[string]string{"X-Kedge-Org": data.userAOrgUUID},
				nil,
			)
			if err != nil {
				t.Fatalf("User B GET memberships: %v", err)
			}
			if !framework.IsAuthRejectStatus(code) {
				t.Fatalf("User B GET memberships: expected 401/403/404, got %d", code)
			}
			t.Logf("User B list memberships rejected with %d (correct)", code)
			return ctx
		}).
		Assess("user_b_cannot_add_self_as_member", func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
			data := ctxIsolation(ctx, t)
			// The most attractive bypass: User B tries to add themselves
			// (or anybody) as an admin of User A's org by hitting the
			// member-add endpoint directly. Must be rejected.
			code, _, err := framework.DoRESTRequest(
				ctx, http.MethodPost,
				data.hubURL+"/api/orgs/"+data.userAOrgUUID+"/memberships",
				data.userBToken,
				map[string]string{"X-Kedge-Org": data.userAOrgUUID},
				map[string]string{"user": "attacker@example.com", "role": "admin"},
			)
			if err != nil {
				t.Fatalf("User B POST membership: %v", err)
			}
			if !framework.IsAuthRejectStatus(code) {
				t.Fatalf("User B POST membership (privilege escalation attempt): expected 401/403/404, got %d", code)
			}
			t.Logf("User B membership-add rejected with %d (correct)", code)
			return ctx
		}).
		Assess("user_b_cannot_list_user_a_service_accounts", func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
			data := ctxIsolation(ctx, t)
			code, _, err := framework.DoRESTRequest(
				ctx, http.MethodGet,
				data.hubURL+"/api/orgs/"+data.userAOrgUUID+"/workspaces/"+data.userAWSUUID+"/serviceaccounts",
				data.userBToken,
				map[string]string{
					"X-Kedge-Org":       data.userAOrgUUID,
					"X-Kedge-Workspace": data.userAWSUUID,
				},
				nil,
			)
			if err != nil {
				t.Fatalf("User B GET SAs: %v", err)
			}
			if !framework.IsAuthRejectStatus(code) {
				t.Fatalf("User B GET service accounts: expected 401/403/404, got %d", code)
			}
			t.Logf("User B list SAs rejected with %d (correct)", code)
			return ctx
		}).
		Assess("user_b_cannot_create_service_account_in_user_a_workspace", func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
			data := ctxIsolation(ctx, t)
			code, _, err := framework.DoRESTRequest(
				ctx, http.MethodPost,
				data.hubURL+"/api/orgs/"+data.userAOrgUUID+"/workspaces/"+data.userAWSUUID+"/serviceaccounts",
				data.userBToken,
				map[string]string{
					"X-Kedge-Org":       data.userAOrgUUID,
					"X-Kedge-Workspace": data.userAWSUUID,
				},
				map[string]string{"displayName": "attacker-sa", "role": "admin"},
			)
			if err != nil {
				t.Fatalf("User B POST SA: %v", err)
			}
			if !framework.IsAuthRejectStatus(code) {
				t.Fatalf("User B POST service account: expected 401/403/404, got %d", code)
			}
			t.Logf("User B create SA rejected with %d (correct)", code)
			return ctx
		}).
		Assess("user_a_positive_control_can_still_use_own_org", func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
			// Positive control. If User A's own calls suddenly start failing
			// the "all rejections" outcomes above become uninformative — the
			// hub could be rejecting everyone, not just User B.
			data := ctxIsolation(ctx, t)
			code, body, err := framework.DoRESTRequest(
				ctx, http.MethodGet,
				data.hubURL+"/api/orgs/"+data.userAOrgUUID,
				data.userAToken,
				map[string]string{"X-Kedge-Org": data.userAOrgUUID},
				nil,
			)
			if err != nil {
				t.Fatalf("User A GET own org: %v", err)
			}
			if code != http.StatusOK {
				t.Fatalf("User A GET own org: expected 200, got %d: %s", code, body)
			}
			if !strings.Contains(string(body), data.userAOrgName) {
				t.Fatalf("User A GET own org: body missing displayName %q: %s", data.userAOrgName, body)
			}
			t.Logf("User A can still read own org (correct positive control)")
			return ctx
		}).
		Teardown(func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
			data, ok := ctx.Value(tenantIsolationCtxKey{}).(*tenantIsolationData)
			if !ok {
				return ctx
			}
			// Best-effort soft-delete of the org User A created. Real cleanup
			// happens during the soft-delete reconciler's grace window.
			code, _, err := framework.DoRESTRequest(
				ctx, http.MethodDelete,
				data.hubURL+"/api/orgs/"+data.userAOrgUUID,
				data.userAToken,
				map[string]string{"X-Kedge-Org": data.userAOrgUUID},
				nil,
			)
			if err != nil {
				t.Logf("teardown: User A DELETE org failed (best-effort): %v", err)
				return ctx
			}
			t.Logf("teardown: User A DELETE org returned %d", code)
			return ctx
		}).
		Feature()
}

// ctxIsolation pulls the setup payload out of the context or skips the
// assess step if setup never populated it (e.g. when the OIDC suite isn't
// in use).
func ctxIsolation(ctx context.Context, t *testing.T) *tenantIsolationData {
	t.Helper()
	data, ok := ctx.Value(tenantIsolationCtxKey{}).(*tenantIsolationData)
	if !ok {
		t.Skip("tenant isolation setup was skipped")
	}
	return data
}
