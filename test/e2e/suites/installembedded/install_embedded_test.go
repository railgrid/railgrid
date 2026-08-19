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

package installembedded

import (
	"context"
	"testing"
	"time"

	"sigs.k8s.io/e2e-framework/pkg/envconf"
	"sigs.k8s.io/e2e-framework/pkg/features"

	"github.com/faroshq/faros/test/e2e/cases"
	"github.com/faroshq/faros/test/e2e/framework"
)

// TestHubHealth mirrors the doc's `curl -k https://faros.localhost:9443/healthz`.
func TestHubHealth(t *testing.T) {
	testenv.Test(t, cases.HubHealth())
}

// TestStaticTokenLogin mirrors the doc's `faros login --token dev-token`.
func TestStaticTokenLogin(t *testing.T) {
	testenv.Test(t, cases.StaticTokenLogin())
}

// Tenancy CRUD proves the embedded kcp actually serves the tenancy plane.
func TestTenancyOrgCRUD(t *testing.T)       { testenv.Test(t, cases.TenancyOrgCRUD()) }
func TestTenancyWorkspaceCRUD(t *testing.T) { testenv.Test(t, cases.TenancyWorkspaceCRUD()) }

// TestHubThroughGateway mirrors the doc's verify step: the hub answers on the
// gateway's SNI route (faros.kcp.localhost) as it would behind real DNS.
func TestHubThroughGateway(t *testing.T) {
	f := features.New("hub through gateway").
		Assess("healthz via SNI route returns 200", func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
			err := framework.Poll(ctx, 5*time.Second, 2*time.Minute, func(ctx context.Context) (bool, error) {
				code, err := framework.GatewayGet(ctx, "faros.kcp.localhost", "/healthz")
				if err != nil {
					t.Logf("gateway route not ready yet: %v", err)
					return false, nil
				}
				return code == 200, nil
			})
			if err != nil {
				t.Fatalf("hub not reachable through gateway SNI route: %v", err)
			}
			return ctx
		}).
		Feature()

	testenv.Test(t, f)
}
