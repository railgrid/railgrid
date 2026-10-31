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
	"testing"
	"time"

	"github.com/gorilla/mux"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	dynamicfake "k8s.io/client-go/dynamic/fake"

	tenancyv1alpha1 "github.com/railgrid/railgrid/apis/tenancy/v1alpha1"
	railgridclient "github.com/railgrid/railgrid/pkg/client"
	"github.com/railgrid/railgrid/pkg/hub/controllers/softdelete"
)

// The undelete routes rewrite tenant state with kcp-admin credentials, so they
// sit behind the platform-admin gate like everything else under /api/admin.
func TestUndeleteRoutesRequirePlatformAdmin(t *testing.T) {
	for _, path := range []string{
		"/api/admin/organizations/org-a/undelete",
		"/api/admin/organizations/org-a/workspaces/ws-1/undelete",
	} {
		for _, tc := range []struct {
			name       string
			user       string
			isAdmin    bool
			wantStatus int
		}{
			{"non-admin", "bob", false, http.StatusForbidden},
			{"unidentified", "", false, http.StatusUnauthorized},
		} {
			t.Run(path+"/"+tc.name, func(t *testing.T) {
				rec := httptest.NewRecorder()
				req := httptest.NewRequest(http.MethodPost, path, nil)
				adminGatedRouter(tc.user, tc.isAdmin).ServeHTTP(rec, req)
				if rec.Code != tc.wantStatus {
					t.Fatalf("status = %d, want %d", rec.Code, tc.wantStatus)
				}
			})
		}
	}
}

// orgRouter mounts the admin routes for an already-admitted admin with a typed
// client over a fake dynamic client seeded with orgs. The grace period is the
// one under test.
func orgRouter(t *testing.T, grace time.Duration, orgs ...*tenancyv1alpha1.Organization) (http.Handler, *railgridclient.Client) {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := tenancyv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("scheme: %v", err)
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
	NewHandler(nil, client, nil).WithSoftDeleteGracePeriod(grace).Register(sub)
	return r, client
}

func softDeletedOrg(name string, requestedAt time.Time) *tenancyv1alpha1.Organization {
	stamp := metav1.NewTime(requestedAt)
	return &tenancyv1alpha1.Organization{
		TypeMeta:   metav1.TypeMeta{APIVersion: tenancyv1alpha1.SchemeGroupVersion.String(), Kind: "Organization"},
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec:       tenancyv1alpha1.OrganizationSpec{DisplayName: name},
		Status:     tenancyv1alpha1.OrganizationStatus{DeletionRequestedAt: &stamp},
	}
}

func postUndelete(t *testing.T, h http.Handler, name string) (*httptest.ResponseRecorder, map[string]any) {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/admin/organizations/"+name+"/undelete", nil)
	h.ServeHTTP(rec, req)
	body := map[string]any{}
	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	return rec, body
}

func TestUndeleteOrganization_WithinWindowClearsStamp(t *testing.T) {
	h, client := orgRouter(t, 72*time.Hour, softDeletedOrg("org-a", time.Now().UTC().Add(-time.Hour)))
	rec, body := postUndelete(t, h, "org-a")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	if body["restored"] != true {
		t.Errorf("restored = %v, want true", body["restored"])
	}
	org, err := client.Organizations().Get(context.Background(), "org-a", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get org: %v", err)
	}
	if org.Status.DeletionRequestedAt != nil {
		t.Errorf("deletionRequestedAt still set: %v", org.Status.DeletionRequestedAt)
	}
}

// Past the window the reconciler is cascading; clearing the stamp under it
// must be refused. The window is the handler's, not a hardcoded 30 days, so a
// hub configured with a shorter one refuses sooner.
func TestUndeleteOrganization_RefusesPastConfiguredWindow(t *testing.T) {
	// 2 days ago: inside the 30-day default, outside a 24h window.
	h, client := orgRouter(t, 24*time.Hour, softDeletedOrg("org-a", time.Now().UTC().Add(-48*time.Hour)))
	rec, _ := postUndelete(t, h, "org-a")
	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409: %s", rec.Code, rec.Body.String())
	}
	org, err := client.Organizations().Get(context.Background(), "org-a", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get org: %v", err)
	}
	if org.Status.DeletionRequestedAt == nil {
		t.Error("deletionRequestedAt was cleared despite the expired window")
	}
}

func TestUndeleteOrganization_DefaultWindowWhenUnset(t *testing.T) {
	// 2 days ago with no configured window: the 30-day default applies.
	h, _ := orgRouter(t, 0, softDeletedOrg("org-a", time.Now().UTC().Add(-48*time.Hour)))
	rec, body := postUndelete(t, h, "org-a")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	if body["restored"] != true {
		t.Errorf("restored = %v, want true", body["restored"])
	}
	if (&Handler{}).GracePeriod() != softdelete.DefaultGracePeriod {
		t.Errorf("GracePeriod fallback = %v, want %v", (&Handler{}).GracePeriod(), softdelete.DefaultGracePeriod)
	}
}

func TestUndeleteOrganization_IdempotentAndNotFound(t *testing.T) {
	live := softDeletedOrg("org-live", time.Now())
	live.Status.DeletionRequestedAt = nil
	h, _ := orgRouter(t, 0, live)

	rec, body := postUndelete(t, h, "org-live")
	if rec.Code != http.StatusOK || body["restored"] != false {
		t.Errorf("live org: status = %d restored = %v, want 200/false", rec.Code, body["restored"])
	}
	rec, _ = postUndelete(t, h, "org-missing")
	if rec.Code != http.StatusNotFound {
		t.Errorf("missing org: status = %d, want 404", rec.Code)
	}
}

// The listing reports when a soft-deleted org or workspace will be purged, so
// the admin UI can show the deadline next to its Restore button. The stamps
// are derived from the handler's configured window, not a hardcoded 30 days.
func TestSoftDeleteStampsUseConfiguredWindow(t *testing.T) {
	requestedAt := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	h := (&Handler{}).WithSoftDeleteGracePeriod(72 * time.Hour)
	requested, purge := h.softDeleteStamps(requestedAt)
	if requested == nil || *requested != "2026-10-01T12:00:00Z" {
		t.Errorf("deletionRequestedAt = %v", requested)
	}
	if purge == nil || *purge != "2026-10-04T12:00:00Z" {
		t.Errorf("purgeAt = %v, want 2026-10-04T12:00:00Z", purge)
	}
	_, purgeDefault := (&Handler{}).softDeleteStamps(requestedAt)
	if purgeDefault == nil || *purgeDefault != requestedAt.Add(softdelete.DefaultGracePeriod).Format(time.RFC3339) {
		t.Errorf("default purgeAt = %v, want +30d", purgeDefault)
	}
}
