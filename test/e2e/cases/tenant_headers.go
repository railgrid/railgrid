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

// Tenant header validation: every /api/orgs/{org}/... call requires the
// caller to set X-Kedge-Org; workspace-scoped calls require both
// X-Kedge-Org and X-Kedge-Workspace. The headers are not optional and
// they must match the URL path — if they don't, that's an attempt to
// fool the tenant middleware and should be rejected.

package cases

import (
	"context"
	"net/http"
	"testing"

	"sigs.k8s.io/e2e-framework/pkg/envconf"
	"sigs.k8s.io/e2e-framework/pkg/features"

	"github.com/faroshq/faros-kedge/test/e2e/framework"
)

// TenancyTenantHeaders verifies the tenant middleware rejects requests
// where the X-Kedge-Org/X-Kedge-Workspace headers are missing, point at a
// non-existent org, or don't match the URL path.
func TenancyTenantHeaders() features.Feature {
	return features.New("Tenancy/TenantHeaders").
		Setup(func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
			hubURL := tenancyHubURL(ctx, t)
			bearer := tenancyBearer(ctx, t)
			org, err := framework.CreateOrgViaREST(ctx, hubURL, bearer, uniqueName("e2e-headers"))
			if err != nil {
				t.Fatalf("setup org: %v", err)
			}
			ws, err := framework.CreateWorkspaceViaREST(ctx, hubURL, bearer, org.UUID, uniqueName("ws"))
			if err != nil {
				t.Fatalf("setup workspace: %v", err)
			}
			return context.WithValue(ctx, tenantHeadersCtxKey{}, &tenantHeadersData{
				hubURL:  hubURL,
				bearer:  bearer,
				orgUUID: org.UUID,
				wsUUID:  ws.UUID,
			})
		}).
		Assess("missing_org_header_rejected_on_workspaces_path", func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
			d := ctx.Value(tenantHeadersCtxKey{}).(*tenantHeadersData)
			// No tenantHeaders argument — caller didn't claim a tenant context.
			code, body, err := framework.DoRESTRequest(ctx, http.MethodGet,
				workspacesURL(d.hubURL, d.orgUUID), d.bearer, nil, nil)
			if err != nil {
				t.Fatalf("GET workspaces no header: %v", err)
			}
			requireReject(t, "GET workspaces no header", code, body)
			return ctx
		}).
		Assess("mismatched_org_header_rejected", func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
			d := ctx.Value(tenantHeadersCtxKey{}).(*tenantHeadersData)
			// URL says d.orgUUID, header says something else. Should be
			// rejected as a forgery attempt.
			code, body, err := framework.DoRESTRequest(ctx, http.MethodGet,
				workspacesURL(d.hubURL, d.orgUUID), d.bearer,
				map[string]string{"X-Kedge-Org": "00000000-0000-0000-0000-000000000000"}, nil)
			if err != nil {
				t.Fatalf("GET workspaces mismatched header: %v", err)
			}
			requireReject(t, "GET workspaces mismatched header", code, body)
			return ctx
		}).
		Assess("nonexistent_org_rejected", func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
			d := ctx.Value(tenantHeadersCtxKey{}).(*tenantHeadersData)
			fakeOrg := "00000000-0000-0000-0000-000000000000"
			code, body, err := framework.DoRESTRequest(ctx, http.MethodGet,
				workspacesURL(d.hubURL, fakeOrg), d.bearer,
				orgHeaders(fakeOrg), nil)
			if err != nil {
				t.Fatalf("GET workspaces nonexistent org: %v", err)
			}
			requireReject(t, "GET workspaces nonexistent org", code, body)
			return ctx
		}).
		Assess("missing_workspace_header_rejected_on_workspace_path", func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
			d := ctx.Value(tenantHeadersCtxKey{}).(*tenantHeadersData)
			// Org header set but workspace header missing on a
			// workspace-scoped path. Behaviour should reject.
			code, body, err := framework.DoRESTRequest(ctx, http.MethodGet,
				workspaceURL(d.hubURL, d.orgUUID, d.wsUUID), d.bearer,
				orgHeaders(d.orgUUID), nil)
			if err != nil {
				t.Fatalf("GET workspace no ws header: %v", err)
			}
			requireReject(t, "GET workspace no ws header", code, body)
			return ctx
		}).
		Assess("mismatched_workspace_header_rejected", func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
			d := ctx.Value(tenantHeadersCtxKey{}).(*tenantHeadersData)
			fakeWS := "11111111-1111-1111-1111-111111111111"
			code, body, err := framework.DoRESTRequest(ctx, http.MethodGet,
				workspaceURL(d.hubURL, d.orgUUID, d.wsUUID), d.bearer,
				map[string]string{
					"X-Kedge-Org":       d.orgUUID,
					"X-Kedge-Workspace": fakeWS,
				}, nil)
			if err != nil {
				t.Fatalf("GET workspace mismatched ws header: %v", err)
			}
			requireReject(t, "GET workspace mismatched ws header", code, body)
			return ctx
		}).
		Teardown(func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
			d, ok := ctx.Value(tenantHeadersCtxKey{}).(*tenantHeadersData)
			if !ok {
				return ctx
			}
			_, _ = framework.DeleteOrgViaREST(context.Background(), d.hubURL, d.bearer, d.orgUUID)
			return ctx
		}).
		Feature()
}

type tenantHeadersCtxKey struct{}

type tenantHeadersData struct {
	hubURL  string
	bearer  string
	orgUUID string
	wsUUID  string
}
