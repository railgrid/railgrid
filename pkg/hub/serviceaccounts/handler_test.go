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

package serviceaccounts

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gorilla/mux"

	tenancyv1alpha1 "github.com/faroshq/faros-kedge/apis/tenancy/v1alpha1"
	"github.com/faroshq/faros-kedge/pkg/hub/tenant"
)

// newTestServer mounts the handler on a fresh router with a stub
// middleware that injects the given TenantContext.
func newTestServer(t *testing.T, tc tenant.TenantContext) (*httptest.Server, *Manager) {
	t.Helper()
	m, _ := managerFor(t)

	h := NewHandler(m)
	r := mux.NewRouter()
	api := r.PathPrefix("/api/orgs").Subrouter()
	// Inject TenantContext directly — we're unit-testing the handler,
	// not the production tenant middleware.
	api.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			next.ServeHTTP(w, req.WithContext(tenant.WithContext(req.Context(), tc)))
		})
	})
	h.Register(api)
	return httptest.NewServer(r), m
}

func adminCtx() tenant.TenantContext {
	return tenant.TenantContext{
		User:          "alice",
		OrgUUID:       "org-1",
		WorkspaceUUID: "ws-1",
		Role:          tenancyv1alpha1.MembershipRoleAdmin,
	}
}

func memberCtx() tenant.TenantContext {
	return tenant.TenantContext{
		User:          "alice",
		OrgUUID:       "org-1",
		WorkspaceUUID: "ws-1",
		Role:          tenancyv1alpha1.MembershipRoleMember,
	}
}

func TestHandler_Create_RequiresAdmin(t *testing.T) {
	srv, _ := newTestServer(t, memberCtx())
	defer srv.Close()
	defer resetTestClientset()

	body, _ := json.Marshal(CreateRequest{DisplayName: "ci", Role: RoleMember})
	resp, err := http.Post(srv.URL+"/api/orgs/org-1/workspaces/ws-1/serviceaccounts", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("status: got %d, want 403", resp.StatusCode)
	}
}

func TestHandler_Create_AdminSucceeds(t *testing.T) {
	srv, _ := newTestServer(t, adminCtx())
	defer srv.Close()
	defer resetTestClientset()

	body, _ := json.Marshal(CreateRequest{DisplayName: "ci-bot", Role: RoleAdmin})
	resp, err := http.Post(srv.URL+"/api/orgs/org-1/workspaces/ws-1/serviceaccounts", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusCreated {
		t.Errorf("status: got %d, want 201", resp.StatusCode)
	}
	var sa SA
	if err := json.NewDecoder(resp.Body).Decode(&sa); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if sa.DisplayName != "ci-bot" || sa.Role != RoleAdmin {
		t.Errorf("body: %#v", sa)
	}
}

func TestHandler_PathHeaderMismatch_400(t *testing.T) {
	tc := adminCtx()
	tc.OrgUUID = "other-org"
	srv, _ := newTestServer(t, tc)
	defer srv.Close()
	defer resetTestClientset()

	body, _ := json.Marshal(CreateRequest{DisplayName: "x", Role: RoleAdmin})
	resp, _ := http.Post(srv.URL+"/api/orgs/org-1/workspaces/ws-1/serviceaccounts", "application/json", bytes.NewReader(body))
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status: got %d, want 400 on path/header mismatch", resp.StatusCode)
	}
}

func TestHandler_Create_InvalidRoleSurfacesAs400(t *testing.T) {
	srv, _ := newTestServer(t, adminCtx())
	defer srv.Close()
	defer resetTestClientset()

	body, _ := json.Marshal(CreateRequest{DisplayName: "x", Role: "viewer"})
	resp, _ := http.Post(srv.URL+"/api/orgs/org-1/workspaces/ws-1/serviceaccounts", "application/json", bytes.NewReader(body))
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status: got %d, want 400", resp.StatusCode)
	}
}

func TestHandler_Patch_EmptyBody400(t *testing.T) {
	srv, _ := newTestServer(t, adminCtx())
	defer srv.Close()
	defer resetTestClientset()

	body, _ := json.Marshal(PatchRequest{})
	req, _ := http.NewRequest(http.MethodPatch, srv.URL+"/api/orgs/org-1/workspaces/ws-1/serviceaccounts/anything", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("PATCH: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status: got %d, want 400", resp.StatusCode)
	}
}

func TestHandler_List_ReturnsItems(t *testing.T) {
	srv, m := newTestServer(t, adminCtx())
	defer srv.Close()
	defer resetTestClientset()

	if _, err := m.Create(t.Context(), "org-1", "ws-1", "first", RoleAdmin); err != nil {
		t.Fatalf("seed Create: %v", err)
	}
	if _, err := m.Create(t.Context(), "org-1", "ws-1", "second", RoleMember); err != nil {
		t.Fatalf("seed Create: %v", err)
	}

	resp, err := http.Get(srv.URL + "/api/orgs/org-1/workspaces/ws-1/serviceaccounts")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("status: got %d, want 200", resp.StatusCode)
	}
	var list ListResponse
	if err := json.NewDecoder(resp.Body).Decode(&list); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(list.Items) != 2 {
		t.Errorf("Items: got %d, want 2", len(list.Items))
	}
}
