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

package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	aiv1alpha1 "github.com/railgrid/provider-app-studio/apis/ai/v1alpha1"
	"github.com/railgrid/provider-sdk/dataplane"
)

func TestProjectIntegrationPatchAuthorizesGrantChangesBeforePersist(t *testing.T) {
	changedDigest := "sha256:" + strings.Repeat("f", 64)
	cases := []struct {
		name             string
		initialGrant     string
		catalogDigest    string
		requestedDigest  string
		denyParent       bool
		authorizationErr error
		allowAction      bool
		wantStatus       int
		wantMinted       int
		wantReviews      int
	}{
		{
			name:         "new action denied",
			initialGrant: "none", catalogDigest: testProjectActionSchemaDigest,
			requestedDigest: testProjectActionSchemaDigest, wantStatus: http.StatusForbidden,
			wantReviews: 2,
		},
		{
			name:            "parent resource denied",
			initialGrant:    "none",
			catalogDigest:   testProjectActionSchemaDigest,
			requestedDigest: testProjectActionSchemaDigest,
			denyParent:      true,
			wantStatus:      http.StatusForbidden,
			wantReviews:     1,
		},
		{
			name:         "authorization unavailable for changed grant",
			initialGrant: "active", catalogDigest: changedDigest, requestedDigest: changedDigest,
			authorizationErr: errors.New("subject access review unavailable"), wantStatus: http.StatusServiceUnavailable,
			wantReviews: 2,
		},
		{
			name:            "reactivation denied",
			initialGrant:    "revoked",
			catalogDigest:   testProjectActionSchemaDigest,
			requestedDigest: testProjectActionSchemaDigest,
			wantStatus:      http.StatusForbidden,
			wantReviews:     2,
		},
		{
			name:         "authorized reactivation",
			initialGrant: "revoked", catalogDigest: testProjectActionSchemaDigest,
			requestedDigest: testProjectActionSchemaDigest, allowAction: true,
			wantStatus: http.StatusOK, wantMinted: 1, wantReviews: 2,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			initial := projectWithTableIntegration(false)
			actions := initial.Spec.Environments[0].Bindings[0].AllowedActions
			switch tc.initialGrant {
			case "none":
				initial.Spec.Environments[0].Bindings[0].AllowedActions = nil
			case "active":
				actions[0].SchemaDigest = testProjectActionSchemaDigest
			case "revoked":
				actions[0].Revoked = true
			case "":
				t.Fatalf("test case has no initial grant state")
			}
			fixture := newIntegrationHTTPFixture(t, initial)
			server := integrationTestServer(t, fixture)
			server.actionsExternalURL = "https://actions.example"
			server.providerActionCatalogResolver = func(context.Context, identity) ([]providerCatalogEntry, error) {
				return []providerCatalogEntry{{
					Name: projectIntegrationProviderDatabricks, Ready: true,
					Export: testDatabricksTableExport([]providerCatalogAction{{
						Name: projectIntegrationActionQueryTable, Version: projectIntegrationActionVersionV1,
						SchemaDigest: tc.catalogDigest,
					}}),
				}}, nil
			}

			var reviews []dataplane.ResourceAttributes
			server.integrationAccessReviewer = func(ctx context.Context, id identity, attrs dataplane.ResourceAttributes) (bool, error) {
				reviews = append(reviews, attrs)
				if allowed, err := integrationTestAccessReview(ctx, id, attrs); err != nil || !allowed {
					return allowed, err
				}
				if attrs.Verb == "get" && tc.denyParent {
					return false, nil
				}
				if attrs.Verb == "create" {
					if tc.authorizationErr != nil {
						return false, tc.authorizationErr
					}
					return tc.allowAction, nil
				}
				return true, nil
			}
			minted := 0
			server.projectIdentityTokenFor = func(context.Context, identity, *aiv1alpha1.Project) (string, error) {
				minted++
				return "project-token", nil
			}

			before := fixture.project(t)
			body := fmt.Sprintf(`{"allowedActions":[{"name":"query_table","version":"v1","schemaDigest":"%s"}]}`, tc.requestedDigest)
			// Use the registered handler so the test exercises the real Project
			// read, authorization checks, and persistence boundary.
			router := newIntegrationRouter(server)
			recorded := integrationHTTPTestRequest("PATCH", "/api/projects/demo/integrations/sales", body)
			result := httptest.NewRecorder()
			router.ServeHTTP(result, recorded)
			if result.Code != tc.wantStatus {
				t.Fatalf("patch status = %d, want %d: %s", result.Code, tc.wantStatus, result.Body.String())
			}

			wantReviews := []dataplane.ResourceAttributes{
				{Group: "databricks.railgrid.ai", Version: "v1alpha1", Resource: "tables", Name: "orders", Verb: "get"},
				{Group: "databricks.railgrid.ai", Version: "v1alpha1", Resource: "tables", Subresource: "query_table", Name: "orders", Verb: "create"},
			}
			wantReviews = wantReviews[:tc.wantReviews]
			if !reflect.DeepEqual(reviews, wantReviews) {
				t.Fatalf("caller authorization reviews = %#v, want %#v", reviews, wantReviews)
			}
			if minted != tc.wantMinted {
				t.Fatalf("Project identity token mint count = %d, want %d", minted, tc.wantMinted)
			}
			if tc.wantStatus != http.StatusOK {
				if after := fixture.project(t); !reflect.DeepEqual(after.Spec, before.Spec) {
					t.Fatalf("Project changed after authorization failure: before %#v after %#v", before.Spec, after.Spec)
				}
				return
			}

			grant := fixture.project(t).Spec.Environments[0].Bindings[0].AllowedActions[0]
			if grant.Revoked || grant.SchemaDigest != tc.requestedDigest || grant.GrantedBy != "alice@example.com" || grant.GrantedAt == nil || grant.RevokedAt != nil || grant.RevokedBy != "" {
				t.Fatalf("persisted reactivated grant = %#v, want fresh caller-owned audit", grant)
			}
		})
	}
}

func TestProjectIntegrationPatchRevocationSkipsCallerAndCatalogChecks(t *testing.T) {
	fixture := newIntegrationHTTPFixture(t, projectWithTableIntegration(false))
	server := integrationTestServer(t, fixture)
	server.actionsExternalURL = "https://actions.example"
	catalogCalls := 0
	server.providerActionCatalogResolver = func(context.Context, identity) ([]providerCatalogEntry, error) {
		catalogCalls++
		return nil, errors.New("provider is unavailable")
	}
	var reviews int
	server.integrationAccessReviewer = func(context.Context, identity, dataplane.ResourceAttributes) (bool, error) {
		reviews++
		return false, errors.New("caller access was withdrawn")
	}
	minted := 0
	server.projectIdentityTokenFor = func(context.Context, identity, *aiv1alpha1.Project) (string, error) {
		minted++
		return "project-token", nil
	}

	router := newIntegrationRouter(server)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, integrationHTTPTestRequest("PATCH", "/api/projects/demo/integrations/sales", fmt.Sprintf(
		`{"allowedActions":[{"name":"query_table","version":"v1","schemaDigest":"%s","revoked":true}]}`,
		testProjectActionSchemaDigest,
	)))
	if response.Code != http.StatusOK {
		t.Fatalf("revoke while caller is denied and provider is unavailable status = %d: %s", response.Code, response.Body.String())
	}
	if reviews != 0 {
		t.Fatalf("revocation made %d caller authorization reviews, want none", reviews)
	}
	if catalogCalls != 0 {
		t.Fatalf("revocation consulted provider catalog %d times, want none", catalogCalls)
	}
	if minted != 1 {
		t.Fatalf("Project identity token mint count = %d, want one refresh after the persisted revocation", minted)
	}
	grant := fixture.project(t).Spec.Environments[0].Bindings[0].AllowedActions[0]
	if !grant.Revoked || grant.RevokedBy != "alice@example.com" || grant.RevokedAt == nil || grant.RevokedAt.IsZero() {
		t.Fatalf("persisted revoked grant = %#v, want server-owned revocation audit", grant)
	}
}

func TestProjectIntegrationPatchMixedRevocationPreservesOtherGrantWhenProviderUnavailable(t *testing.T) {
	initial := projectWithTableIntegration(false)
	secondGrantAt := metav1.NewTime(time.Date(2025, 1, 2, 3, 4, 5, 0, time.UTC))
	initial.Spec.Environments[0].Bindings[0].AllowedActions = append(
		initial.Spec.Environments[0].Bindings[0].AllowedActions,
		aiv1alpha1.ProjectProviderActionSpec{
			Name: "update_table", Version: "v1", SchemaDigest: testProjectActionSchemaDigest,
			GrantedBy: "alice@example.com", GrantedAt: &secondGrantAt,
		},
	)
	fixture := newIntegrationHTTPFixture(t, initial)
	server := integrationTestServer(t, fixture)
	server.actionsExternalURL = "https://actions.example"
	catalogCalls := 0
	server.providerActionCatalogResolver = func(context.Context, identity) ([]providerCatalogEntry, error) {
		catalogCalls++
		return nil, errors.New("provider catalog is unavailable")
	}
	var reviews int
	server.integrationAccessReviewer = func(context.Context, identity, dataplane.ResourceAttributes) (bool, error) {
		reviews++
		return false, nil
	}
	minted := 0
	server.projectIdentityTokenFor = func(context.Context, identity, *aiv1alpha1.Project) (string, error) {
		minted++
		return "project-token", nil
	}
	before := fixture.project(t)
	beforeActions := before.Spec.Environments[0].Bindings[0].AllowedActions

	body := fmt.Sprintf(`{"allowedActions":[{"name":"query_table","version":"v1","schemaDigest":"%s","revoked":true},{"name":"update_table","version":"v1","schemaDigest":"%s"}]}`,
		testProjectActionSchemaDigest, testProjectActionSchemaDigest,
	)
	response := httptest.NewRecorder()
	newIntegrationRouter(server).ServeHTTP(response, integrationHTTPTestRequest("PATCH", "/api/projects/demo/integrations/sales", body))
	if response.Code != http.StatusOK {
		t.Fatalf("mixed revoke with provider unavailable and caller denied status = %d: %s", response.Code, response.Body.String())
	}
	if catalogCalls != 0 || reviews != 0 {
		t.Fatalf("mixed revoke made catalog/reviewer calls = %d/%d, want none", catalogCalls, reviews)
	}
	if minted != 1 {
		t.Fatalf("Project identity token mint count = %d, want one refresh after the persisted revocation", minted)
	}

	after := fixture.project(t).Spec.Environments[0].Bindings[0].AllowedActions
	if len(after) != 2 {
		t.Fatalf("persisted grants = %#v, want revoked query_table and unchanged update_table", after)
	}
	byName := make(map[string]aiv1alpha1.ProjectProviderActionSpec, len(after))
	for _, grant := range after {
		byName[grant.Name] = grant
	}
	if grant := byName["update_table"]; !reflect.DeepEqual(grant, beforeActions[1]) {
		t.Fatalf("unchanged active grant changed during revocation: before %#v after %#v", beforeActions[1], grant)
	}
	revoked := byName["query_table"]
	if !revoked.Revoked || revoked.RevokedBy != "alice@example.com" || revoked.RevokedAt == nil || revoked.RevokedAt.IsZero() {
		t.Fatalf("revoked grant = %#v, want server-owned revocation audit", revoked)
	}
	if revoked.GrantedBy != beforeActions[0].GrantedBy || revoked.GrantedAt == nil || beforeActions[0].GrantedAt == nil || !revoked.GrantedAt.Equal(beforeActions[0].GrantedAt) {
		t.Fatalf("revocation changed original grant audit: before %#v after %#v", beforeActions[0], revoked)
	}
}

// integrationTestAccessReview is a bounded reviewer for the fake table used
// by integration tests. It rejects unknown callers, objects, verbs and action
// coordinates instead of granting every request made by a fixture.
func integrationTestAccessReview(_ context.Context, id identity, attrs dataplane.ResourceAttributes) (bool, error) {
	if id.caller == nil || id.caller.User == "" || id.caller.User != id.user ||
		attrs.Group != "databricks.railgrid.ai" || attrs.Version != "v1alpha1" || attrs.Resource != "tables" {
		return false, nil
	}
	if attrs.Name != "orders" && attrs.Name != "customers" {
		return false, nil
	}
	switch attrs.Verb {
	case "get":
		return attrs.Subresource == "", nil
	case "create":
		return attrs.Subresource == "query_table" || attrs.Subresource == "update_table", nil
	default:
		return false, nil
	}
}

func TestProjectIntegrationAuthorizationDeltaSkipsUnchangedAndRevokedGrants(t *testing.T) {
	existing := []aiv1alpha1.ProjectProviderActionSpec{
		{Name: "unchanged", Version: "v1", SchemaDigest: testProjectActionSchemaDigest},
		{Name: "revoked", Version: "v1", SchemaDigest: testProjectActionSchemaDigest},
		{Name: "removed", Version: "v1", SchemaDigest: testProjectActionSchemaDigest},
	}
	merged := []aiv1alpha1.ProjectProviderActionSpec{
		{Name: "unchanged", Version: "v1", SchemaDigest: testProjectActionSchemaDigest},
		{Name: "revoked", Version: "v1", SchemaDigest: testProjectActionSchemaDigest, Revoked: true},
		{Name: "added", Version: "v1", SchemaDigest: testProjectActionSchemaDigest},
	}
	want := []aiv1alpha1.ProjectProviderActionSpec{{Name: "added", Version: "v1", SchemaDigest: testProjectActionSchemaDigest}}
	if got := projectIntegrationActionsRequiringAuthorization(existing, merged); !reflect.DeepEqual(got, want) {
		t.Fatalf("authorization delta = %#v, want %#v", got, want)
	}
}

func TestProjectIntegrationActionPatchRestrictionOnlyRequiresActualReduction(t *testing.T) {
	active := aiv1alpha1.ProjectProviderActionSpec{
		Name: "query_table", Version: "v1", SchemaDigest: testProjectActionSchemaDigest,
	}
	revoked := active
	revoked.Revoked = true
	revokedPrior := active
	revokedPrior.Revoked = true
	existing := map[string]aiv1alpha1.ProjectProviderActionSpec{
		projectProviderActionKey(active.Name, active.Version): active,
		projectProviderActionKey("update_table", "v1"): {
			Name: "update_table", Version: "v1", SchemaDigest: testProjectActionSchemaDigest,
		},
	}
	changed := active
	changed.SchemaDigest = "sha256:" + strings.Repeat("f", 64)
	reactivatedExisting := map[string]aiv1alpha1.ProjectProviderActionSpec{
		projectProviderActionKey(active.Name, active.Version): revokedPrior,
	}

	for _, tc := range []struct {
		name     string
		existing map[string]aiv1alpha1.ProjectProviderActionSpec
		desired  []aiv1alpha1.ProjectProviderActionSpec
		want     bool
	}{
		{name: "no-op", existing: existing, desired: []aiv1alpha1.ProjectProviderActionSpec{active, existing[projectProviderActionKey("update_table", "v1")]}, want: false},
		{name: "active grant revoked", existing: existing, desired: []aiv1alpha1.ProjectProviderActionSpec{revoked, existing[projectProviderActionKey("update_table", "v1")]}, want: true},
		{name: "grant removed", existing: existing, desired: []aiv1alpha1.ProjectProviderActionSpec{active}, want: true},
		{name: "digest changed", existing: existing, desired: []aiv1alpha1.ProjectProviderActionSpec{changed}, want: false},
		{name: "reactivated", existing: reactivatedExisting, desired: []aiv1alpha1.ProjectProviderActionSpec{active}, want: false},
		{name: "new grant", existing: existing, desired: []aiv1alpha1.ProjectProviderActionSpec{active, {Name: "new_action", Version: "v1", SchemaDigest: testProjectActionSchemaDigest}}, want: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := projectIntegrationActionPatchIsRestrictionOnly(tc.existing, tc.desired); got != tc.want {
				t.Fatalf("restriction-only = %t, want %t", got, tc.want)
			}
		})
	}
}
