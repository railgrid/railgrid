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

package metering

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gorilla/mux"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/klog/v2"
)

func plan(name string, limits ...map[string]any) *unstructured.Unstructured {
	u := &unstructured.Unstructured{}
	u.SetAPIVersion("metering.contrib.kcp.io/v1alpha1")
	u.SetKind("Plan")
	u.SetName(name)
	rows := make([]any, 0, len(limits))
	for _, l := range limits {
		rows = append(rows, l)
	}
	_ = unstructured.SetNestedField(u.Object, map[string]any{"workspaces": map[string]any{"eventName": "ws"}}, "spec", "backend")
	_ = unstructured.SetNestedSlice(u.Object, rows, "spec", "limits")
	return u
}

func entitlement(name, planName string) *unstructured.Unstructured {
	u := &unstructured.Unstructured{}
	u.SetAPIVersion("metering.contrib.kcp.io/v1alpha1")
	u.SetKind("Entitlement")
	u.SetName(name)
	_ = unstructured.SetNestedField(u.Object, name, "spec", "account")
	_ = unstructured.SetNestedField(u.Object, planName, "spec", "plan")
	return u
}

type adminFixture struct {
	ws     dynamic.Interface
	router *mux.Router
}

func newAdminFixture(t *testing.T, installedNow bool, objs ...runtime.Object) adminFixture {
	t.Helper()
	listKinds := map[schema.GroupVersionResource]string{
		gvrPlans:             "PlanList",
		gvrEntitlements:      "EntitlementList",
		gvrAccounts:          "AccountList",
		gvrMembershipReports: "MembershipReportList",
	}
	ws := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), listKinds, objs...)
	d := &Discovery{workspace: "root:railgrid:system:metering", logger: klog.Background()}
	if installedNow {
		d.set(&Endpoints{PortalURL: "http://metering"})
	}
	h := NewHandler(d).WithWriter(NewWriter(ws, "free", func(context.Context) string { return "admin@example.org" }, klog.Background()))
	r := mux.NewRouter()
	h.Register(r)
	return adminFixture{ws: ws, router: r}
}

func (f adminFixture) do(t *testing.T, method, path, body string) (int, map[string]any) {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	rec := httptest.NewRecorder()
	f.router.ServeHTTP(rec, req)
	out := map[string]any{}
	if rec.Body.Len() > 0 {
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Fatalf("%s %s: non-JSON body %q", method, path, rec.Body.String())
		}
	}
	return rec.Code, out
}

func TestEntitlementPlanChange(t *testing.T) {
	f := newAdminFixture(t, true, plan("free"), plan("team"), entitlement("org-1", "free"))

	code, out := f.do(t, http.MethodPut, "/metering/entitlements/org-1", `{"plan":"team","overrides":[{"meter":"agents","shape":"Level","enforce":"Hard","max":"10"}]}`)
	if code != http.StatusOK {
		t.Fatalf("status %d: %v", code, out)
	}
	ent, err := f.ws.Resource(gvrEntitlements).Get(context.Background(), "org-1", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if p, _, _ := unstructured.NestedString(ent.Object, "spec", "plan"); p != "team" {
		t.Fatalf("plan = %q, want team", p)
	}
	if acct, _, _ := unstructured.NestedString(ent.Object, "spec", "account"); acct != "org-1" {
		t.Fatalf("account was lost: %q", acct)
	}
	overrides, _, _ := unstructured.NestedSlice(ent.Object, "spec", "overrides")
	if len(overrides) != 1 || overrides[0].(map[string]any)["max"] != "10" {
		t.Fatalf("overrides = %v", overrides)
	}

	// Clearing the overrides drops the field rather than writing [].
	code, _ = f.do(t, http.MethodPut, "/metering/entitlements/org-1", `{"plan":"team","overrides":[]}`)
	if code != http.StatusOK {
		t.Fatalf("status %d", code)
	}
	ent, _ = f.ws.Resource(gvrEntitlements).Get(context.Background(), "org-1", metav1.GetOptions{})
	if _, found, _ := unstructured.NestedSlice(ent.Object, "spec", "overrides"); found {
		t.Fatalf("empty overrides were written")
	}
}

func TestEntitlementRejections(t *testing.T) {
	f := newAdminFixture(t, true, plan("free"), entitlement("org-1", "free"))
	tests := []struct {
		name, path, body string
		want             int
		msg              string
	}{
		{"unknown plan", "/metering/entitlements/org-1", `{"plan":"gold","overrides":[]}`, http.StatusBadRequest, `plan "gold" does not exist`},
		{"missing plan", "/metering/entitlements/org-1", `{"overrides":[]}`, http.StatusBadRequest, "plan is required"},
		{"unknown entitlement", "/metering/entitlements/nope", `{"plan":"free"}`, http.StatusNotFound, "not found"},
		{"hard limit without max", "/metering/entitlements/org-1", `{"plan":"free","overrides":[{"meter":"agents","enforce":"Hard"}]}`, http.StatusBadRequest, "needs a max"},
		{"bad max", "/metering/entitlements/org-1", `{"plan":"free","overrides":[{"meter":"agents","enforce":"Hard","max":"ten"}]}`, http.StatusBadRequest, "non-negative decimal"},
		{"negative max", "/metering/entitlements/org-1", `{"plan":"free","overrides":[{"meter":"agents","enforce":"Soft","max":"-1"}]}`, http.StatusBadRequest, "non-negative decimal"},
		{"bad enforce", "/metering/entitlements/org-1", `{"plan":"free","overrides":[{"meter":"agents","enforce":"Block","max":"1"}]}`, http.StatusBadRequest, "enforce must be"},
		{"bad shape", "/metering/entitlements/org-1", `{"plan":"free","overrides":[{"meter":"agents","shape":"Rate","enforce":"Hard","max":"1"}]}`, http.StatusBadRequest, "shape must be"},
		{"bad meter", "/metering/entitlements/org-1", `{"plan":"free","overrides":[{"meter":"Agents Count","enforce":"Hard","max":"1"}]}`, http.StatusBadRequest, "must be a slug"},
		{"duplicate meter", "/metering/entitlements/org-1", `{"plan":"free","overrides":[{"meter":"a","enforce":"Hard","max":"1"},{"meter":"a","enforce":"Soft","max":"2"}]}`, http.StatusBadRequest, "listed twice"},
		{"unknown field", "/metering/entitlements/org-1", `{"plan":"free","account":"other"}`, http.StatusBadRequest, "invalid body"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			code, out := f.do(t, http.MethodPut, tc.path, tc.body)
			if code != tc.want || !strings.Contains(out["error"].(string), tc.msg) {
				t.Fatalf("got %d %v, want %d containing %q", code, out, tc.want, tc.msg)
			}
		})
	}
	ent, _ := f.ws.Resource(gvrEntitlements).Get(context.Background(), "org-1", metav1.GetOptions{})
	if p, _, _ := unstructured.NestedString(ent.Object, "spec", "plan"); p != "free" {
		t.Fatalf("a rejected edit changed the plan to %q", p)
	}
}

func TestPlanCreateUpdateDelete(t *testing.T) {
	f := newAdminFixture(t, true, plan("free", map[string]any{"meter": "workspaces", "shape": "Level", "enforce": "Hard", "max": "1"}), entitlement("org-1", "free"))

	code, out := f.do(t, http.MethodPut, "/metering/plans/team", `{"displayName":"Team","limits":[{"meter":"workspaces","shape":"Level","enforce":"Hard","max":"25"},{"meter":"cpu_hours","enforce":"Meter"}]}`)
	if code != http.StatusCreated {
		t.Fatalf("create: %d %v", code, out)
	}

	// Updating keeps the backend mapping the editor does not know about.
	code, out = f.do(t, http.MethodPut, "/metering/plans/free", `{"displayName":"Free","limits":[{"meter":"workspaces","shape":"Level","enforce":"Hard","max":"3"}]}`)
	if code != http.StatusOK {
		t.Fatalf("update: %d %v", code, out)
	}
	free, _ := f.ws.Resource(gvrPlans).Get(context.Background(), "free", metav1.GetOptions{})
	if _, found, _ := unstructured.NestedMap(free.Object, "spec", "backend"); !found {
		t.Fatalf("spec.backend was dropped by the update")
	}
	limits, _, _ := unstructured.NestedSlice(free.Object, "spec", "limits")
	if len(limits) != 1 || limits[0].(map[string]any)["max"] != "3" {
		t.Fatalf("limits = %v", limits)
	}

	if code, out = f.do(t, http.MethodPut, "/metering/plans/Not_A_Label", `{"limits":[]}`); code != http.StatusBadRequest {
		t.Fatalf("bad name: %d %v", code, out)
	}

	// The default plan and a plan in use stay.
	if code, out = f.do(t, http.MethodDelete, "/metering/plans/free", ""); code != http.StatusConflict || !strings.Contains(out["error"].(string), "default plan") {
		t.Fatalf("delete default: %d %v", code, out)
	}
	f.do(t, http.MethodPut, "/metering/entitlements/org-1", `{"plan":"team"}`)
	if code, out = f.do(t, http.MethodDelete, "/metering/plans/team", ""); code != http.StatusConflict || !strings.Contains(out["error"].(string), "org-1") {
		t.Fatalf("delete in-use: %d %v", code, out)
	}
	f.do(t, http.MethodPut, "/metering/entitlements/org-1", `{"plan":"free"}`)
	if code, _ = f.do(t, http.MethodDelete, "/metering/plans/team", ""); code != http.StatusNoContent {
		t.Fatalf("delete: %d", code)
	}
	if code, _ = f.do(t, http.MethodDelete, "/metering/plans/team", ""); code != http.StatusNotFound {
		t.Fatalf("delete again: %d", code)
	}
}

func TestWritesRefusedUntilInstalled(t *testing.T) {
	f := newAdminFixture(t, false, plan("free"), entitlement("org-1", "free"))
	code, out := f.do(t, http.MethodPut, "/metering/entitlements/org-1", `{"plan":"free"}`)
	if code != http.StatusServiceUnavailable {
		t.Fatalf("got %d %v", code, out)
	}
	code, out = f.do(t, http.MethodGet, "/metering", "")
	if code != http.StatusOK || out["writable"] != true || out["defaultPlan"] != "free" {
		t.Fatalf("status: %d %v", code, out)
	}
}
