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

// End-to-end coverage for per-workspace access through the hub's front door
// to kcp. The hub forwards /clusters/{id} requests to kcp unchanged, so who
// may enter a workspace is kcp's decision: the `access` entry gate and the
// tenant-admin binding the hub writes for each Membership. Two distinct users
// each own a workspace; each reaches their own cluster, is refused the
// other's by kcp, and gains it once a Membership grants cross access.
// Requires the OIDC suite for two identities.

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

	"github.com/railgrid/railgrid/test/e2e/framework"
)

// clusterGet issues a raw request through the hub front door (no tenant
// headers — nothing in the hub reads them on this path) for a harmless read
// in the target cluster, and returns kcp's answer.
func clusterGet(ctx context.Context, hubURL, clusterID, bearer string) (int, string) {
	url := strings.TrimRight(hubURL, "/") + "/clusters/" + clusterID + "/apis/apis.kcp.io/v1alpha2/apibindings"
	code, body, err := framework.DoRESTRequest(ctx, http.MethodGet, url, bearer, nil, nil)
	if err != nil {
		return 0, err.Error()
	}
	return code, string(body)
}

// kcpRefused reports whether kcp turned the caller away at the workspace:
// a 403 is the `access` gate (or RBAC below it), and a 401 means kcp did not
// accept the credential at all. Either way the hub made no decision.
func kcpRefused(code int) bool {
	return code == http.StatusForbidden || code == http.StatusUnauthorized
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
func waitClusterAllowed(ctx context.Context, t *testing.T, hubURL, clusterID, bearer, who string) {
	t.Helper()
	deadline := time.Now().Add(90 * time.Second)
	var lastCode int
	var lastBody string
	for time.Now().Before(deadline) {
		code, body := clusterGet(ctx, hubURL, clusterID, bearer)
		lastCode, lastBody = code, body
		if code == http.StatusOK {
			return
		}
		time.Sleep(2 * time.Second)
	}
	t.Fatalf("%s never gained access to cluster %s (last code=%d body=%s)", who, clusterID, lastCode, lastBody)
}

// WorkspaceAccessIsolation verifies that kcp alone gates workspace access
// through the hub: each user reaches only the workspace clusters their
// Memberships have RBAC for, cross-user access is refused by kcp until
// granted, and a request that names no workspace is not served as if it had.
func WorkspaceAccessIsolation() features.Feature {
	return features.New("Tenancy/WorkspaceAccessIsolation").
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
			// (org-scope admin membership is mirrored into each child
			// workspace as a tenant-admin binding; waitClusterAllowed absorbs
			// the moment between workspace Ready and that binding landing).
			waitClusterAllowed(ctx, t, hubURL, cidA, resA.IDToken, "userA→wsA")
			waitClusterAllowed(ctx, t, hubURL, cidB, resB.IDToken, "userB→wsB")
			// ── Isolation: neither user may reach the other's cluster ────────
			// The refusal is kcp's: the user holds no RBAC in that workspace,
			// so the `access` gate denies before any resource is consulted.
			if code, body := clusterGet(ctx, hubURL, cidB, resA.IDToken); !kcpRefused(code) {
				t.Fatalf("userA→userB workspace: expected kcp to refuse, got code=%d body=%s", code, body)
			}
			if code, body := clusterGet(ctx, hubURL, cidA, resB.IDToken); !kcpRefused(code) {
				t.Fatalf("userB→userA workspace: expected kcp to refuse, got code=%d body=%s", code, body)
			}
			t.Log("cross-user cluster access refused by kcp in both directions (correct)")
			// ── A path that names no workspace is not served as one ──────────
			// The hub no longer substitutes a default cluster; whatever kcp
			// does with a bare path, it must not hand back the user's data.
			bareURL := strings.TrimRight(hubURL, "/") + "/apis/apis.kcp.io/v1alpha2/apibindings"
			if code, _, err := framework.DoRESTRequest(ctx, http.MethodGet, bareURL, resA.IDToken, nil, nil); err != nil || code == http.StatusOK {
				t.Fatalf("bare path: expected a refusal, got code=%d err=%v", code, err)
			}
			t.Log("bare path not served as a workspace")
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
			waitClusterAllowed(ctx, t, hubURL, cidA, resB.IDToken, "userB→wsA (after grant)")
			t.Log("workspace-scope membership grants User B access to wsA (correct)")

			return ctx
		}).
		Feature()
}
