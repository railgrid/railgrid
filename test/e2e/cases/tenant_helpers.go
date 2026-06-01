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

// Shared helpers for the tenancy-area e2e cases. Each case follows the
// same pattern: log in, exercise REST endpoints, assert. The helpers
// here keep that boilerplate out of the assertions themselves.

package cases

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/faroshq/faros-kedge/test/e2e/framework"
)

// tenancyBearer obtains a bearer token suitable for the hub REST surface,
// adapting to whichever suite is active:
//   - OIDC: performs a HeadlessOIDCLogin as the primary Dex user.
//   - Static-token suites (Standalone, External KCP, SSH): returns the
//     framework's dev token.
//
// Callers should not write conditionals on the suite themselves; the
// helper picks the right credential for the environment.
func tenancyBearer(ctx context.Context, t *testing.T) string {
	t.Helper()
	dex := framework.DexEnvFrom(ctx)
	if dex != nil {
		loginCtx, cancel := context.WithTimeout(ctx, 90*time.Second)
		defer cancel()
		result, err := framework.HeadlessOIDCLogin(loginCtx, framework.ClusterEnvFrom(ctx).HubURL, dex.UserEmail, dex.UserPassword)
		if err != nil {
			t.Fatalf("OIDC login failed: %v", err)
		}
		if result.IDToken == "" {
			t.Fatal("OIDC login returned empty ID token")
		}
		return result.IDToken
	}
	if framework.DevToken == "" {
		t.Skip("no static dev-token and no Dex env; cannot obtain bearer")
	}
	return framework.DevToken
}

// tenancyHubURL returns the hub URL from the cluster env, or skips when
// the env is missing (suite not initialised).
func tenancyHubURL(ctx context.Context, t *testing.T) string {
	t.Helper()
	clusterEnv := framework.ClusterEnvFrom(ctx)
	if clusterEnv == nil {
		t.Skip("cluster environment not found in context")
	}
	return clusterEnv.HubURL
}

// requireStatus is a tiny assertion that fatals with a useful diagnostic
// when the response code doesn't match. Most of the tenancy assertions
// boil down to "did the surface return the right status?", so doing this
// once per call site rather than 4 lines of if-Fatalf is a clarity win.
func requireStatus(t *testing.T, name string, wantCode, gotCode int, body []byte) {
	t.Helper()
	if gotCode != wantCode {
		t.Fatalf("%s: expected %d, got %d (body=%s)", name, wantCode, gotCode, body)
	}
}

// requireOK accepts any successful status code (200 or 204) — used for
// DELETE/PATCH endpoints where the backend may legitimately return
// either depending on whether it has a body to send back.
func requireOK(t *testing.T, name string, gotCode int, body []byte) {
	t.Helper()
	if gotCode != http.StatusOK && gotCode != http.StatusNoContent {
		t.Fatalf("%s: expected 200 or 204, got %d (body=%s)", name, gotCode, body)
	}
}

// requireReject is the negative analogue: any of 400/401/403/404 is
// acceptable; a 2xx is the bug. 400 covers the validation path the
// tenant middleware takes for missing/malformed headers — that's a
// rejection just like a 403 from an auth check.
func requireReject(t *testing.T, name string, gotCode int, body []byte) {
	t.Helper()
	if gotCode != http.StatusBadRequest && !framework.IsAuthRejectStatus(gotCode) {
		t.Fatalf("%s: expected 400/401/403/404, got %d (body=%s)", name, gotCode, body)
	}
}

// orgURL / workspaceURL / saURL build the REST paths a tenancy test
// hits, so a typo in the path lives in one place instead of every case.
func orgURL(hubURL, orgUUID string) string {
	return hubURL + "/api/orgs/" + orgUUID
}

func workspaceURL(hubURL, orgUUID, wsUUID string) string {
	return hubURL + "/api/orgs/" + orgUUID + "/workspaces/" + wsUUID
}

func workspacesURL(hubURL, orgUUID string) string {
	return hubURL + "/api/orgs/" + orgUUID + "/workspaces"
}

func saListURL(hubURL, orgUUID, wsUUID string) string {
	return hubURL + "/api/orgs/" + orgUUID + "/workspaces/" + wsUUID + "/serviceaccounts"
}

func saURL(hubURL, orgUUID, wsUUID, saUUID string) string {
	return saListURL(hubURL, orgUUID, wsUUID) + "/" + saUUID
}

func saTokenURL(hubURL, orgUUID, wsUUID, saUUID string) string {
	return saURL(hubURL, orgUUID, wsUUID, saUUID) + "/tokens"
}

// orgWSHeaders returns the X-Kedge-{Org,Workspace} headers a
// workspace-scoped REST call needs.
func orgWSHeaders(orgUUID, wsUUID string) map[string]string {
	return map[string]string{
		"X-Kedge-Org":       orgUUID,
		"X-Kedge-Workspace": wsUUID,
	}
}

func orgHeaders(orgUUID string) map[string]string {
	return map[string]string{"X-Kedge-Org": orgUUID}
}

// _ status code constants exposed as ints. Unused locally but keep the
// import alive for tests that import the cases package indirectly.
var _ = http.StatusOK
