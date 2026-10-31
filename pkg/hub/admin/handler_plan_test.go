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

package admin

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gorilla/mux"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	dynamicfake "k8s.io/client-go/dynamic/fake"

	tenancyv1alpha1 "github.com/railgrid/railgrid/apis/tenancy/v1alpha1"
	railgridclient "github.com/railgrid/railgrid/pkg/client"
)

func TestSetOrganizationPlanRequiresPlatformAdmin(t *testing.T) {
	for _, tc := range []struct {
		user       string
		isAdmin    bool
		wantStatus int
	}{{"bob", false, http.StatusForbidden}, {"", false, http.StatusUnauthorized}} {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPut, "/api/admin/organizations/org-a/plan", strings.NewReader(`{"plan":"team"}`))
		adminGatedRouter(tc.user, tc.isAdmin).ServeHTTP(rec, req)
		if rec.Code != tc.wantStatus {
			t.Fatalf("%q: status = %d, want %d", tc.user, rec.Code, tc.wantStatus)
		}
	}
}

func planRouter(t *testing.T, lookup func(context.Context, string) (bool, error), orgs ...*tenancyv1alpha1.Organization) (http.Handler, *railgridclient.Client) {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := tenancyv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	objs := make([]runtime.Object, 0, len(orgs))
	for _, o := range orgs {
		objs = append(objs, o)
	}
	client := railgridclient.NewFromDynamic(dynamicfake.NewSimpleDynamicClient(scheme, objs...))
	r := mux.NewRouter()
	sub := r.PathPrefix("/api/admin").Subrouter()
	sub.Use(Middleware(
		UserResolverFunc(func(*http.Request) (string, error) { return "root", nil }),
		AdminCheckerFunc(func(context.Context, string) bool { return true }),
	))
	h := NewHandler(nil, client, nil)
	if lookup != nil {
		h.WithPlanLookup(lookup)
	}
	h.Register(sub)
	return r, client
}

func putPlan(h http.Handler, org, body string) (*httptest.ResponseRecorder, map[string]any) {
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPut, "/api/admin/organizations/"+org+"/plan", strings.NewReader(body)))
	out := map[string]any{}
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return rec, out
}

func TestSetOrganizationPlan(t *testing.T) {
	org := &tenancyv1alpha1.Organization{
		TypeMeta:   metav1.TypeMeta{APIVersion: tenancyv1alpha1.SchemeGroupVersion.String(), Kind: "Organization"},
		ObjectMeta: metav1.ObjectMeta{Name: "org-a", Labels: map[string]string{tenancyv1alpha1.OrganizationCreatorLabel: "alice"}},
		Spec:       tenancyv1alpha1.OrganizationSpec{DisplayName: "A"},
	}
	known := func(_ context.Context, name string) (bool, error) { return name == "team", nil }
	h, client := planRouter(t, known, org)

	// Detach onto an existing plan: the org now owns its account.
	rec, body := putPlan(h, "org-a", `{"plan":"team"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	if body["account"] != "org-a" || body["ownAccount"] != true || body["plan"] != "team" {
		t.Fatalf("body = %v", body)
	}
	got, _ := client.Organizations().Get(context.Background(), "org-a", metav1.GetOptions{})
	if got.Spec.Plan != "team" {
		t.Fatalf("spec.plan = %q", got.Spec.Plan)
	}

	// Unknown plan is refused and nothing changes.
	rec, _ = putPlan(h, "org-a", `{"plan":"platinum"}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("unknown plan: status = %d", rec.Code)
	}
	got, _ = client.Organizations().Get(context.Background(), "org-a", metav1.GetOptions{})
	if got.Spec.Plan != "team" {
		t.Fatalf("spec.plan changed to %q on a refused request", got.Spec.Plan)
	}

	// Empty pools the org back into its creator's account.
	rec, body = putPlan(h, "org-a", `{"plan":""}`)
	if rec.Code != http.StatusOK || body["account"] != "user-alice" || body["ownAccount"] != false {
		t.Fatalf("pool back: status = %d body = %v", rec.Code, body)
	}

	rec, _ = putPlan(h, "missing", `{"plan":"team"}`)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("missing org: status = %d", rec.Code)
	}
}

func TestSetOrganizationPlanWithoutMetering(t *testing.T) {
	h, _ := planRouter(t, nil, &tenancyv1alpha1.Organization{ObjectMeta: metav1.ObjectMeta{Name: "org-a"}})
	rec, _ := putPlan(h, "org-a", `{"plan":"team"}`)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", rec.Code)
	}
}
