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

package restapi

import (
	"context"
	"net/http"
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	tenancyv1alpha1 "github.com/railgrid/railgrid/apis/tenancy/v1alpha1"
)

func TestOrganizationAPICannotEnableMemberProviderManagement(t *testing.T) {
	org := &tenancyv1alpha1.Organization{
		ObjectMeta: metav1.ObjectMeta{Name: "org-a"},
		Spec: tenancyv1alpha1.OrganizationSpec{
			DisplayName: "A", CatalogEntryCreation: tenancyv1alpha1.CatalogEntryCreationAdmin,
		},
	}
	mgr, _, _ := newTestManager(t, org)
	srv := newTestServer(t, mgr, adminTC("alice", "org-a", ""))
	defer srv.Close()
	for _, method := range []string{http.MethodPost, http.MethodPatch} {
		t.Run(method, func(t *testing.T) {
			path := "/api/orgs"
			if method == http.MethodPatch {
				path += "/org-a"
			}
			req, err := http.NewRequest(method, srv.URL+path,
				strings.NewReader(`{"displayName":"new name","catalogEntryCreation":"members"}`))
			if err != nil {
				t.Fatal(err)
			}
			req.Header.Set("Content-Type", "application/json")
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = resp.Body.Close() }()
			if resp.StatusCode != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400", resp.StatusCode)
			}
			stored, err := mgr.client.Organizations().List(context.Background(), metav1.ListOptions{})
			if err != nil {
				t.Fatal(err)
			}
			if len(stored.Items) != 1 || stored.Items[0].Spec.DisplayName != "A" || stored.Items[0].Spec.CatalogEntryCreation != tenancyv1alpha1.CatalogEntryCreationAdmin {
				t.Fatalf("rejected policy changed organization state: %+v", stored.Items)
			}
		})
	}
}

func TestOrganizationViewReportsEffectiveProviderPolicy(t *testing.T) {
	for _, legacy := range []string{"", tenancyv1alpha1.CatalogEntryCreationMembers, tenancyv1alpha1.CatalogEntryCreationAdmin} {
		org := &tenancyv1alpha1.Organization{Spec: tenancyv1alpha1.OrganizationSpec{CatalogEntryCreation: legacy}}
		if got := projectOrg(org).CatalogEntryCreation; got != tenancyv1alpha1.CatalogEntryCreationAdmin {
			t.Errorf("stored policy %q: effective policy = %q, want admin", legacy, got)
		}
	}
}
