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
	"errors"
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
	return planRouterWithReset(t, lookup, nil, orgs...)
}

func planRouterWithReset(t *testing.T, lookup func(context.Context, string) (bool, error), reset func(context.Context, string) error, orgs ...*tenancyv1alpha1.Organization) (http.Handler, *railgridclient.Client) {
	t.Helper()
	return planRouterWithWriters(t, lookup, reset, nil, orgs...)
}

func planRouterWithWriters(t *testing.T, lookup func(context.Context, string) (bool, error), reset func(context.Context, string) error, assign func(context.Context, string, string) error, orgs ...*tenancyv1alpha1.Organization) (http.Handler, *railgridclient.Client) {
	t.Helper()
	return planRouterWithArchiveLookup(t, lookup, nil, reset, assign, orgs...)
}

func planRouterWithArchiveLookup(t *testing.T, lookup func(context.Context, string) (bool, error), archived func(context.Context, string) (bool, error), reset func(context.Context, string) error, assign func(context.Context, string, string) error, orgs ...*tenancyv1alpha1.Organization) (http.Handler, *railgridclient.Client) {
	return planRouterWithValidation(t, lookup, archived, reset, assign, nil, orgs...)
}

func planRouterWithDefaultValidation(t *testing.T, lookup func(context.Context, string) (bool, error), validate func(context.Context) error, reset func(context.Context, string) error, orgs ...*tenancyv1alpha1.Organization) (http.Handler, *railgridclient.Client) {
	return planRouterWithValidation(t, lookup, nil, reset, nil, validate, orgs...)
}

func planRouterWithValidation(t *testing.T, lookup func(context.Context, string) (bool, error), archived func(context.Context, string) (bool, error), reset func(context.Context, string) error, assign func(context.Context, string, string) error, validate func(context.Context) error, orgs ...*tenancyv1alpha1.Organization) (http.Handler, *railgridclient.Client) {
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
	if archived != nil {
		h.WithArchivedPlanLookup(archived)
	}
	if reset != nil {
		h.WithPlanReset(reset)
	}
	if validate != nil {
		h.WithDefaultPlanValidation(validate)
	}
	if assign != nil {
		h.WithPlanAssignment(assign)
	}
	h.Register(sub)
	return r, client
}

func TestSetOrganizationPlanValidatesDefaultBeforeClearingIntent(t *testing.T) {
	org := &tenancyv1alpha1.Organization{
		TypeMeta:   metav1.TypeMeta{APIVersion: tenancyv1alpha1.SchemeGroupVersion.String(), Kind: "Organization"},
		ObjectMeta: metav1.ObjectMeta{Name: "org-a"},
		Spec:       tenancyv1alpha1.OrganizationSpec{Plan: "team"},
	}
	resetCalls := 0
	validate := func(context.Context) error { return errors.New(`configured default plan "team" is archived`) }
	reset := func(context.Context, string) error {
		resetCalls++
		return nil
	}
	h, client := planRouterWithDefaultValidation(t,
		func(context.Context, string) (bool, error) { return true, nil }, validate, reset, org)

	rec, _ := putPlan(h, "org-a", `{"plan":""}`)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503: %s", rec.Code, rec.Body.String())
	}
	got, err := client.Organizations().Get(context.Background(), "org-a", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if got.Spec.Plan != "team" {
		t.Fatalf("default-plan preflight failure cleared Organization.spec.plan: %q", got.Spec.Plan)
	}
	if resetCalls != 0 {
		t.Fatalf("reset ran after failed preflight: %d calls", resetCalls)
	}
}

// An empty plan resets the Entitlement even when spec.plan was already
// empty: a plan set on the Entitlement directly never reached spec.plan, so
// clearing the field alone would change nothing.
func TestSetOrganizationPlanEmptyResetsTheEntitlement(t *testing.T) {
	org := &tenancyv1alpha1.Organization{
		TypeMeta:   metav1.TypeMeta{APIVersion: tenancyv1alpha1.SchemeGroupVersion.String(), Kind: "Organization"},
		ObjectMeta: metav1.ObjectMeta{Name: "org-a"},
		Spec:       tenancyv1alpha1.OrganizationSpec{DisplayName: "A"},
	}
	known := func(_ context.Context, name string) (bool, error) { return name == "team", nil }
	var resets []string
	var resetErr error
	reset := func(_ context.Context, name string) error {
		resets = append(resets, name)
		return resetErr
	}
	h, _ := planRouterWithValidation(t, known, nil, reset, nil, func(context.Context) error { return nil }, org)

	// spec.plan is empty and stays empty; the reset still runs.
	rec, _ := putPlan(h, "org-a", `{"plan":""}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	if len(resets) != 1 || resets[0] != "org-a" {
		t.Fatalf("resets = %v, want [org-a]", resets)
	}

	// Assigning a plan does not reset.
	if rec, _ = putPlan(h, "org-a", `{"plan":"team"}`); rec.Code != http.StatusOK {
		t.Fatalf("assign: status = %d", rec.Code)
	}
	if len(resets) != 1 {
		t.Fatalf("resets after assign = %v", resets)
	}

	// A reset the metering workspace refuses is reported, with the field
	// already cleared.
	resetErr = errors.New("metering is not installed")
	rec, _ = putPlan(h, "org-a", `{"plan":""}`)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("failed reset: status = %d: %s", rec.Code, rec.Body.String())
	}
	if len(resets) != 2 {
		t.Fatalf("resets = %v", resets)
	}
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

	// Put the organization on an existing plan.
	rec, body := putPlan(h, "org-a", `{"plan":"team"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	if body["name"] != "org-a" || body["plan"] != "team" {
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

	// Empty returns the organization to the default plan.
	rec, body = putPlan(h, "org-a", `{"plan":""}`)
	if rec.Code != http.StatusOK || body["plan"] != "" {
		t.Fatalf("back to the default plan: status = %d body = %v", rec.Code, body)
	}
	got, _ = client.Organizations().Get(context.Background(), "org-a", metav1.GetOptions{})
	if got.Spec.Plan != "" {
		t.Fatalf("spec.plan = %q, want empty", got.Spec.Plan)
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

func TestArchivedOrganizationPlanRetryIsIdempotentButMissingPlanIsRejected(t *testing.T) {
	org := &tenancyv1alpha1.Organization{
		TypeMeta:   metav1.TypeMeta{APIVersion: tenancyv1alpha1.SchemeGroupVersion.String(), Kind: "Organization"},
		ObjectMeta: metav1.ObjectMeta{Name: "org-a"},
		Spec:       tenancyv1alpha1.OrganizationSpec{Plan: "team"},
	}
	newOrg := &tenancyv1alpha1.Organization{
		TypeMeta:   metav1.TypeMeta{APIVersion: tenancyv1alpha1.SchemeGroupVersion.String(), Kind: "Organization"},
		ObjectMeta: metav1.ObjectMeta{Name: "org-b"},
	}
	lookup := func(_ context.Context, name string) (bool, error) { return name != "team" && name != "missing", nil }
	archived := func(_ context.Context, name string) (bool, error) { return name == "team", nil }
	assignments := 0
	assign := func(context.Context, string, string) error { assignments++; return nil }
	h, client := planRouterWithArchiveLookup(t, lookup, archived, nil, assign, org, newOrg)

	// A previously recorded organization assignment can finish after archival.
	if rec, _ := putPlan(h, "org-a", `{"plan":"team"}`); rec.Code != http.StatusOK || assignments != 1 {
		t.Fatalf("archived-plan retry: status %d, assignments %d, body %s", rec.Code, assignments, rec.Body.String())
	}
	if rec, _ := putPlan(h, "org-b", `{"plan":"team"}`); rec.Code != http.StatusBadRequest || assignments != 1 {
		t.Fatalf("new archived-plan assignment: status %d, assignments %d, body %s", rec.Code, assignments, rec.Body.String())
	}

	// A dangling reference is not enough to make a missing plan assignable.
	if rec, _ := putPlan(h, "org-a", `{"plan":"missing"}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("missing plan: status %d, body %s", rec.Code, rec.Body.String())
	}
	got, err := client.Organizations().Get(context.Background(), "org-a", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if got.Spec.Plan != "team" {
		t.Fatalf("spec.plan changed after missing-plan rejection: %q", got.Spec.Plan)
	}
}

func TestAcceptedPlanAssignmentFinishesAfterPlanArchive(t *testing.T) {
	org := &tenancyv1alpha1.Organization{
		TypeMeta:   metav1.TypeMeta{APIVersion: tenancyv1alpha1.SchemeGroupVersion.String(), Kind: "Organization"},
		ObjectMeta: metav1.ObjectMeta{Name: "org-a"},
	}
	archived := false
	lookup := func(_ context.Context, name string) (bool, error) {
		if name != "team" {
			return false, nil
		}
		active := !archived
		// Model archival immediately after the active-plan validation read.
		archived = true
		return active, nil
	}
	assignments := 0
	assign := func(_ context.Context, _, plan string) error {
		if !archived {
			t.Fatal("assignment ran before the plan was archived")
		}
		if plan != "team" {
			t.Fatalf("assignment plan = %q", plan)
		}
		assignments++
		return nil
	}
	h, client := planRouterWithWriters(t, lookup, nil, assign, org)
	rec, _ := putPlan(h, "org-a", `{"plan":"team"}`)
	if rec.Code != http.StatusOK || assignments != 1 {
		t.Fatalf("in-flight assignment: status %d, assignments %d, body %s", rec.Code, assignments, rec.Body.String())
	}
	got, err := client.Organizations().Get(context.Background(), "org-a", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if got.Spec.Plan != "team" {
		t.Fatalf("organization assignment was not recorded: %q", got.Spec.Plan)
	}
}

func TestSetOrganizationPlanWritesEntitlementAndRetriesPartialFailure(t *testing.T) {
	org := &tenancyv1alpha1.Organization{
		TypeMeta:   metav1.TypeMeta{APIVersion: tenancyv1alpha1.SchemeGroupVersion.String(), Kind: "Organization"},
		ObjectMeta: metav1.ObjectMeta{Name: "org-a"},
		Spec:       tenancyv1alpha1.OrganizationSpec{DisplayName: "A"},
	}
	calls := 0
	var assignErr = errors.New("store unavailable")
	var client *railgridclient.Client
	assign := func(ctx context.Context, name, plan string) error {
		saved, err := client.Organizations().Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return err
		}
		if saved.Spec.Plan != plan {
			t.Fatalf("entitlement write ran before organization assignment: %q", saved.Spec.Plan)
		}
		calls++
		return assignErr
	}
	var h http.Handler
	h, client = planRouterWithWriters(t, func(context.Context, string) (bool, error) { return true, nil }, nil, assign, org)
	rec, _ := putPlan(h, "org-a", `{"plan":"team"}`)
	if rec.Code != http.StatusServiceUnavailable || !strings.Contains(rec.Body.String(), "retry this assignment") {
		t.Fatalf("partial failure: %d %s", rec.Code, rec.Body.String())
	}
	assignErr = nil
	rec, _ = putPlan(h, "org-a", `{"plan":"team"}`)
	if rec.Code != http.StatusOK || calls != 2 {
		t.Fatalf("same-plan retry: status %d, calls %d", rec.Code, calls)
	}
}
