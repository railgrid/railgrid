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

// Soft-delete observable invariants: after the caller soft-deletes their
// own Org, the next /api/orgs list must omit it (the REST layer hides
// soft-deleted rows from the picker per docs/organizations.md §Delete
// an Org). Undelete must restore visibility.

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

// TenancySoftDeleteHidesOrg verifies the UMI-driven org list does not
// expose soft-deleted orgs to the caller, but does expose them again
// after undelete. This is the observable contract; whether the marker
// lives on the Org CR or on the UMI entry is implementation detail.
func TenancySoftDeleteHidesOrg() features.Feature {
	return features.New("Tenancy/SoftDeleteHidesOrg").
		Assess("soft_deleted_org_hidden_then_restored_on_undelete", func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
			hubURL := tenancyHubURL(ctx, t)
			bearer := tenancyBearer(ctx, t)

			org, err := framework.CreateOrgViaREST(ctx, hubURL, bearer, uniqueName("e2e-softdelete"))
			if err != nil {
				t.Fatalf("setup org: %v", err)
			}
			t.Cleanup(func() {
				_, _ = framework.DeleteOrgViaREST(context.Background(), hubURL, bearer, org.UUID)
			})

			// Sanity: org is in the list before deletion.
			if !orgListed(t, ctx, hubURL, bearer, org.UUID) {
				t.Fatalf("freshly-created org %s missing from /api/orgs", org.UUID)
			}

			// Soft-delete.
			code, body, err := framework.DoRESTRequest(ctx, http.MethodDelete,
				orgURL(hubURL, org.UUID), bearer,
				orgHeaders(org.UUID), nil)
			if err != nil {
				t.Fatalf("DELETE org: %v", err)
			}
			requireStatus(t, "DELETE org", http.StatusOK, code, body)

			// Wait for the soft-delete reconciler to propagate the marker
			// to the UMI — list reflects UMI, not the Org CR directly.
			pollCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
			defer cancel()
			pollErr := framework.Poll(pollCtx, 2*time.Second, 30*time.Second, func(ctx context.Context) (bool, error) {
				return !orgListed(t, ctx, hubURL, bearer, org.UUID), nil
			})
			if pollErr != nil {
				t.Fatalf("soft-deleted org never disappeared from list: %v", pollErr)
			}

			// Undelete: org reappears in list.
			code, body, err = framework.DoRESTRequest(ctx, http.MethodPost,
				orgURL(hubURL, org.UUID)+"/undelete", bearer,
				orgHeaders(org.UUID), nil)
			if err != nil {
				t.Fatalf("undelete: %v", err)
			}
			requireStatus(t, "POST undelete", http.StatusOK, code, body)

			pollCtx2, cancel2 := context.WithTimeout(ctx, 30*time.Second)
			defer cancel2()
			pollErr = framework.Poll(pollCtx2, 2*time.Second, 30*time.Second, func(ctx context.Context) (bool, error) {
				return orgListed(t, ctx, hubURL, bearer, org.UUID), nil
			})
			if pollErr != nil {
				t.Fatalf("undeleted org never reappeared in list: %v", pollErr)
			}
			return ctx
		}).
		Feature()
}

// orgListed performs a GET /api/orgs and reports whether the given UUID
// is in the response.
func orgListed(t *testing.T, ctx context.Context, hubURL, bearer, orgUUID string) bool {
	t.Helper()
	code, body, err := framework.DoRESTRequest(ctx, http.MethodGet,
		hubURL+"/api/orgs", bearer, nil, nil)
	if err != nil || code != http.StatusOK {
		return false
	}
	var list struct {
		Items []struct {
			UUID string `json:"uuid"`
		} `json:"items"`
	}
	if err := json.Unmarshal(body, &list); err != nil {
		return false
	}
	for _, o := range list.Items {
		if o.UUID == orgUUID {
			return true
		}
	}
	return false
}
