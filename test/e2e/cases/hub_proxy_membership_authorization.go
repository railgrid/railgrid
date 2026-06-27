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

// End-to-end coverage for the membership-gated kcp proxy (Option A,
// docs/hub-proxy-workspace-access.md). Two distinct users each own a
// workspace; the hub kcp proxy must let each reach their own workspace cluster
// (/clusters/{id}) and refuse the other's with "cluster access denied", until a
// Membership grants cross access. Requires the OIDC suite for two identities.

package cases

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"sigs.k8s.io/e2e-framework/pkg/envconf"
	"sigs.k8s.io/e2e-framework/pkg/features"

	"github.com/faroshq/faros-kedge/test/e2e/framework"
)

// proxyDeniedMessage is the exact message the proxy's membership gate returns
// (pkg/server/proxy/proxy.go clusterAccessDeniedBody). Asserting on it isolates
// "the proxy refused" from any downstream kcp auth/RBAC result, so the test
// doesn't depend on whether kcp trusts Dex in this suite.
const proxyDeniedMessage = "cluster access denied"

// proxyClusterGet issues a raw kcp-proxy request (no tenant headers — the proxy
// authorizes from the path + bearer) for a harmless read in the target cluster.
func proxyClusterGet(ctx context.Context, hubURL, clusterID, bearer string) (int, string) {
	url := strings.TrimRight(hubURL, "/") + "/clusters/" + clusterID + "/apis/apis.kcp.io/v1alpha2/apibindings"
	code, body, err := framework.DoRESTRequest(ctx, http.MethodGet, url, bearer, nil, nil)
	if err != nil {
		return 0, err.Error()
	}
	return code, string(body)
}

// proxyDenied reports whether the response is the proxy's own membership refusal
// (as opposed to a downstream kcp 401/403 with a different body).
func proxyDenied(code int, body string) bool {
	return code == http.StatusForbidden && strings.Contains(body, proxyDeniedMessage)
}

// workspaceClusterID polls GET /api/orgs/{org}/workspaces/{ws} until the
// workspace reports its logical-cluster id (set once it reaches Ready).
func workspaceClusterID(ctx context.Context, t *testing.T, hubURL, bearer, org, ws string) string {
	t.Helper()
	deadline := time.Now().Add(90 * time.Second)
	var lastCode int
	var lastBody string
	for time.Now().Before(deadline) {
		code, body, err := framework.DoRESTRequest(ctx, http.MethodGet, workspaceURL(hubURL, org, ws), bearer, orgWSHeaders(org, ws), nil)
		lastCode, lastBody = code, string(body)
		if err == nil && code == http.StatusOK {
			var v struct {
				ClusterName string `json:"clusterName"`
			}
			if json.Unmarshal(body, &v) == nil && v.ClusterName != "" {
				return v.ClusterName
			}
		}
		time.Sleep(2 * time.Second)
	}
	t.Fatalf("workspace %s/%s never reported a clusterName (last code=%d body=%s)", org, ws, lastCode, lastBody)
	return ""
}

// waitProxyAllowed polls until the proxy stops refusing access (Membership
// reconciled into the caller's UserMembershipIndex).
func waitProxyAllowed(ctx context.Context, t *testing.T, hubURL, clusterID, bearer, who string) {
	t.Helper()
	deadline := time.Now().Add(90 * time.Second)
	var lastCode int
	var lastBody string
	for time.Now().Before(deadline) {
		code, body := proxyClusterGet(ctx, hubURL, clusterID, bearer)
		lastCode, lastBody = code, body
		if !proxyDenied(code, body) {
			return
		}
		time.Sleep(2 * time.Second)
	}
	t.Fatalf("%s never gained proxy access to cluster %s (last code=%d body=%s)", who, clusterID, lastCode, lastBody)
}

// HubProxyMembershipIsolation verifies the membership-gated proxy: each user
// reaches only the workspace clusters their UserMembershipIndex covers, with
// cross-user access refused until granted, and bare paths rejected.
func HubProxyMembershipIsolation() features.Feature {
	return features.New("Tenancy/ProxyMembershipIsolation").
		Assess("membership_gates_cluster_access", func(ctx context.Context, t *testing.T, _ *envconf.Config) context.Context {
			dex := framework.DexEnvFrom(ctx)
			if dex == nil || dex.User2Email == "" {
				t.Skip("requires OIDC suite with a second Dex user")
			}
			hubURL := tenancyHubURL(ctx, t)

			// ── User A: org + workspace ──────────────────────────────────────
			loginA, cancelA := context.WithTimeout(ctx, 90*time.Second)
			defer cancelA()
			resA, err := framework.HeadlessOIDCLogin(loginA, hubURL, dex.UserEmail, dex.UserPassword)
			if err != nil {
				t.Fatalf("User A login: %v", err)
			}
			orgA, err := framework.CreateOrgViaREST(ctx, hubURL, resA.IDToken, uniqueName("e2e-proxy-iso-a"))
			if err != nil {
				t.Fatalf("orgA: %v", err)
			}
			t.Cleanup(func() { _, _ = framework.DeleteOrgViaREST(context.Background(), hubURL, resA.IDToken, orgA.UUID) })
			wsA, err := framework.CreateWorkspaceViaREST(ctx, hubURL, resA.IDToken, orgA.UUID, "wsA")
			if err != nil {
				t.Fatalf("wsA: %v", err)
			}
			cidA := workspaceClusterID(ctx, t, hubURL, resA.IDToken, orgA.UUID, wsA.UUID)

			// ── User B: org + workspace ──────────────────────────────────────
			loginB, cancelB := context.WithTimeout(ctx, 90*time.Second)
			defer cancelB()
			resB, err := framework.HeadlessOIDCLogin(loginB, hubURL, dex.User2Email, dex.User2Password)
			if err != nil {
				t.Fatalf("User B login: %v", err)
			}
			orgB, err := framework.CreateOrgViaREST(ctx, hubURL, resB.IDToken, uniqueName("e2e-proxy-iso-b"))
			if err != nil {
				t.Fatalf("orgB: %v", err)
			}
			t.Cleanup(func() { _, _ = framework.DeleteOrgViaREST(context.Background(), hubURL, resB.IDToken, orgB.UUID) })
			wsB, err := framework.CreateWorkspaceViaREST(ctx, hubURL, resB.IDToken, orgB.UUID, "wsB")
			if err != nil {
				t.Fatalf("wsB: %v", err)
			}
			cidB := workspaceClusterID(ctx, t, hubURL, resB.IDToken, orgB.UUID, wsB.UUID)

			// ── Baseline: each user reaches their own workspace cluster ──────
			// (org-scope admin membership covers the org's child workspaces).
			waitProxyAllowed(ctx, t, hubURL, cidA, resA.IDToken, "userA→wsA")
			waitProxyAllowed(ctx, t, hubURL, cidB, resB.IDToken, "userB→wsB")

			// ── Isolation: neither user may reach the other's cluster ────────
			if code, body := proxyClusterGet(ctx, hubURL, cidB, resA.IDToken); !proxyDenied(code, body) {
				t.Fatalf("userA→userB workspace: expected %q, got code=%d body=%s", proxyDeniedMessage, code, body)
			}
			if code, body := proxyClusterGet(ctx, hubURL, cidA, resB.IDToken); !proxyDenied(code, body) {
				t.Fatalf("userB→userA workspace: expected %q, got code=%d body=%s", proxyDeniedMessage, code, body)
			}
			t.Log("cross-user cluster access refused in both directions (correct)")

			// ── Bare path is rejected (no DefaultCluster default, A-1) ───────
			bareURL := strings.TrimRight(hubURL, "/") + "/apis/apis.kcp.io/v1alpha2/apibindings"
			if code, body, err := framework.DoRESTRequest(ctx, http.MethodGet, bareURL, resA.IDToken, nil, nil); err != nil || code != http.StatusBadRequest {
				t.Fatalf("bare path: expected 400 (no default), got code=%d err=%v body=%s", code, err, body)
			}
			t.Log("bare path rejected with 400 (no DefaultCluster default)")

			// ── Grant: workspace-scope Membership opens cross access ─────────
			// The membership endpoint keys off the target's User CR name (it
			// does Users().Get(req.User)), which the login surfaces as UserID.
			wsMembershipsURL := workspaceURL(hubURL, orgA.UUID, wsA.UUID) + "/memberships"
			code, body, err := framework.DoRESTRequest(ctx, http.MethodPost, wsMembershipsURL, resA.IDToken,
				orgWSHeaders(orgA.UUID, wsA.UUID),
				map[string]string{"user": resB.UserID, "role": "member"})
			if err != nil || code != http.StatusCreated {
				t.Fatalf("grant User B membership in wsA: code=%d err=%v body=%s", code, err, body)
			}
			waitProxyAllowed(ctx, t, hubURL, cidA, resB.IDToken, "userB→wsA (after grant)")
			t.Log("workspace-scope membership grants User B access to wsA (correct)")

			return ctx
		}).
		Feature()
}
