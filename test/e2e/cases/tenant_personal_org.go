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

// Personal org behavior at the REST surface.
//
// docs/organizations.md §Personal Org pins that personal orgs cascade off
// the User CR — soft-deleting the User auto-soft-deletes the personal
// org. The doc does NOT pin whether the user can independently soft-delete
// their own personal org via /api/orgs/{org}, nor whether self-leave on
// the personal org is allowed. The current backend allows both: a personal
// org's owner can stamp deletionRequestedAt on it just like any other org,
// and the soft-delete reconciler handles the rest.
//
// The portal UI refuses these operations on personal orgs for UX
// reasons (you'd have no org to switch to), but that's a UI policy, not an
// API policy. These tests lock in the API contract as it stands today;
// if a future PR adds an API-level guard, this file flips its expectations.

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

// TenancyPersonalOrgSoftDelete verifies the API contract for the
// caller's personal org: the bootstrap creates one on first /api/orgs
// call, a direct DELETE on it is accepted (the soft-delete reconciler
// picks it up), and Undelete restores it within the grace window. Pins
// the API behavior so a future guardrail addition has to update this
// test rather than silently change behavior.
func TenancyPersonalOrgSoftDelete() features.Feature {
	return features.New("Tenancy/PersonalOrgSoftDelete").
		Setup(func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
			hubURL := tenancyHubURL(ctx, t)
			bearer := tenancyBearer(ctx, t)
			// The first /api/orgs call triggers the personal-org bootstrap.
			// Retry briefly so we don't race with the controller.
			var personalOrg string
			pollCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
			defer cancel()
			pollErr := framework.Poll(pollCtx, 2*time.Second, 30*time.Second, func(ctx context.Context) (bool, error) {
				p, err := framework.FindPersonalOrgUUID(ctx, hubURL, bearer)
				if err != nil {
					return false, nil
				}
				personalOrg = p
				return true, nil
			})
			if pollErr != nil {
				t.Fatalf("personal org never appeared: %v", pollErr)
			}
			t.Logf("personal org uuid=%s", personalOrg)
			return context.WithValue(ctx, personalOrgCtxKey{}, &personalOrgData{
				hubURL:  hubURL,
				bearer:  bearer,
				orgUUID: personalOrg,
			})
		}).
		Assess("personal_org_soft_delete_then_undelete", func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
			d := ctx.Value(personalOrgCtxKey{}).(*personalOrgData)

			// Soft-delete the personal org. Backend accepts (200 with the
			// updated projection); deletionRequestedAt should be set.
			code, body, err := framework.DoRESTRequest(ctx, http.MethodDelete,
				orgURL(d.hubURL, d.orgUUID), d.bearer,
				orgHeaders(d.orgUUID), nil)
			if err != nil {
				t.Fatalf("DELETE personal org: %v", err)
			}
			requireStatus(t, "DELETE personal org", http.StatusOK, code, body)
			var afterDelete struct {
				Personal            bool    `json:"personal"`
				DeletionRequestedAt *string `json:"deletionRequestedAt"`
			}
			if err := json.Unmarshal(body, &afterDelete); err != nil {
				t.Fatalf("decoding DELETE response: %v", err)
			}
			if !afterDelete.Personal {
				t.Fatalf("expected personal=true on DELETE response; body=%s", body)
			}
			if afterDelete.DeletionRequestedAt == nil || *afterDelete.DeletionRequestedAt == "" {
				t.Fatalf("expected deletionRequestedAt set; body=%s", body)
			}

			// Undelete restores the org so the suite can continue running
			// without leaving the caller's personal org marked for cascade.
			code, body, err = framework.DoRESTRequest(ctx, http.MethodPost,
				orgURL(d.hubURL, d.orgUUID)+"/undelete", d.bearer,
				orgHeaders(d.orgUUID), nil)
			if err != nil {
				t.Fatalf("undelete personal org: %v", err)
			}
			requireStatus(t, "POST personal org undelete", http.StatusOK, code, body)
			var afterUndelete struct {
				DeletionRequestedAt *string `json:"deletionRequestedAt"`
			}
			if err := json.Unmarshal(body, &afterUndelete); err != nil {
				t.Fatalf("decoding undelete response: %v", err)
			}
			if afterUndelete.DeletionRequestedAt != nil && *afterUndelete.DeletionRequestedAt != "" {
				t.Fatalf("expected deletionRequestedAt cleared; body=%s", body)
			}
			return ctx
		}).
		Feature()
}

type personalOrgCtxKey struct{}

type personalOrgData struct {
	hubURL  string
	bearer  string
	orgUUID string
}
