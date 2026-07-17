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

package restapi

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/mux"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	dynamicfake "k8s.io/client-go/dynamic/fake"

	tenancyv1alpha1 "github.com/faroshq/faros-kedge/apis/tenancy/v1alpha1"
	kedgeclient "github.com/faroshq/faros-kedge/pkg/client"
	"github.com/faroshq/faros-kedge/pkg/hub/kcp"
	hubproviders "github.com/faroshq/faros-kedge/pkg/hub/providers"
	"github.com/faroshq/faros-kedge/pkg/hub/tenant"
)

// ===== fakes =====

type fakeOps struct {
	mu sync.Mutex

	// Storage
	orgWorkspaces     map[string]bool              // orgUUID set
	orgMemberships    map[string]map[string]string // orgUUID → user → role
	childWorkspaces   map[string]map[string]bool   // orgUUID → wsUUID set
	wsDisplayNames    map[wsKey]string             // (org,ws) → display
	wsDeletionAnnos   map[wsKey]time.Time          // (org,ws) → timestamp
	mcpServerCalls    map[wsKey]int                // (org,ws) → count
	kedgeBindingCalls map[wsKey]int                // (org,ws) → count
	workspaceAdmins   map[wsKey]map[string]bool    // (org,ws) → rbacIdentity set
	providerBindings  map[wsKey]map[string]string  // (org,ws) → provider → binding name
	providerBindCalls map[wsKey]int                // (org,ws) → count
}

type wsKey struct{ Org, WS string }

func newFakeOps() *fakeOps {
	return &fakeOps{
		orgWorkspaces:     map[string]bool{},
		orgMemberships:    map[string]map[string]string{},
		childWorkspaces:   map[string]map[string]bool{},
		wsDisplayNames:    map[wsKey]string{},
		wsDeletionAnnos:   map[wsKey]time.Time{},
		mcpServerCalls:    map[wsKey]int{},
		kedgeBindingCalls: map[wsKey]int{},
		workspaceAdmins:   map[wsKey]map[string]bool{},
		providerBindings:  map[wsKey]map[string]string{},
		providerBindCalls: map[wsKey]int{},
	}
}

func (f *fakeOps) EnsureOrgWorkspace(_ context.Context, orgUUID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.orgWorkspaces[orgUUID] = true
	return nil
}

func (f *fakeOps) EnsureOrgMembership(_ context.Context, orgUUID, userName, role string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.orgMemberships[orgUUID] == nil {
		f.orgMemberships[orgUUID] = map[string]string{}
	}
	f.orgMemberships[orgUUID][userName] = role
	return nil
}

func (f *fakeOps) ListOrgMemberships(_ context.Context, orgUUID string) ([]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]string, 0, len(f.orgMemberships[orgUUID]))
	for u := range f.orgMemberships[orgUUID] {
		out = append(out, u)
	}
	return out, nil
}

func (f *fakeOps) GetOrgMembershipRole(_ context.Context, orgUUID, userName string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if m, ok := f.orgMemberships[orgUUID]; ok {
		if role, ok := m[userName]; ok {
			return role, nil
		}
	}
	return "", fmt.Errorf("membership %s in org %s not found", userName, orgUUID)
}

func (f *fakeOps) PatchOrgMembershipRole(_ context.Context, orgUUID, userName, role string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.orgMemberships[orgUUID]; !ok {
		return fmt.Errorf("org %s not found", orgUUID)
	}
	f.orgMemberships[orgUUID][userName] = role
	return nil
}

func (f *fakeOps) DeleteOrgMembership(_ context.Context, orgUUID, userName string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if m, ok := f.orgMemberships[orgUUID]; ok {
		delete(m, userName)
	}
	return nil
}

func (f *fakeOps) EnsureChildWorkspace(_ context.Context, orgUUID, wsUUID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.childWorkspaces[orgUUID] == nil {
		f.childWorkspaces[orgUUID] = map[string]bool{}
	}
	f.childWorkspaces[orgUUID][wsUUID] = true
	return nil
}

func (f *fakeOps) EnsureChildWorkspaceKedgeBinding(_ context.Context, orgUUID, wsUUID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.kedgeBindingCalls[wsKey{orgUUID, wsUUID}]++
	return nil
}

func (f *fakeOps) EnsureChildWorkspaceDefaultMCPServer(_ context.Context, orgUUID, wsUUID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.mcpServerCalls[wsKey{orgUUID, wsUUID}]++
	return nil
}

// EnsureProviderAPIBinding is the test stub for the server-side
// provider-enable handler. The handler is exercised via its own
// dedicated tests; for the existing org/workspace flows it just needs
// to not error.
func (f *fakeOps) EnsureProviderAPIBinding(_ context.Context, orgUUID, wsUUID, bindingName, _, _ string, _ []kcp.ProviderClaim) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	key := wsKey{orgUUID, wsUUID}
	if f.providerBindings[key] == nil {
		f.providerBindings[key] = map[string]string{}
	}
	f.providerBindings[key][bindingName] = bindingName
	f.providerBindCalls[key]++
	return nil
}

// ListProviderAPIBindings is the test stub for the read-side provider-enable
// handler. It returns a copy so handlers cannot mutate fake state by accident.
func (f *fakeOps) ListProviderAPIBindings(_ context.Context, orgUUID, wsUUID string) (map[string]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	key := wsKey{orgUUID, wsUUID}
	out := make(map[string]string, len(f.providerBindings[key]))
	for providerName, bindingName := range f.providerBindings[key] {
		out[providerName] = bindingName
	}
	return out, nil
}

func (f *fakeOps) DeleteProviderAPIBinding(_ context.Context, orgUUID, wsUUID, providerName string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.providerBindings[wsKey{orgUUID, wsUUID}], providerName)
	return nil
}

func (f *fakeOps) EnsureProviderEdgeProxyGrant(_ context.Context, _, _, _, _ string) error {
	return nil
}

func (f *fakeOps) RemoveProviderEdgeProxyGrant(_ context.Context, _, _, _ string) error {
	return nil
}

func (f *fakeOps) ListChildWorkspaces(_ context.Context, orgUUID string) ([]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]string, 0, len(f.childWorkspaces[orgUUID]))
	for ws := range f.childWorkspaces[orgUUID] {
		out = append(out, ws)
	}
	return out, nil
}

func (f *fakeOps) GetWorkspaceDisplayName(_ context.Context, orgUUID, wsUUID string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.childWorkspaces[orgUUID][wsUUID]; !ok {
		return "", fmt.Errorf("workspace not found")
	}
	return f.wsDisplayNames[wsKey{orgUUID, wsUUID}], nil
}

func (f *fakeOps) SetWorkspaceDisplayName(_ context.Context, orgUUID, wsUUID, displayName string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.wsDisplayNames[wsKey{orgUUID, wsUUID}] = displayName
	return nil
}

func (f *fakeOps) GetWorkspaceDeletionRequestedAt(_ context.Context, orgUUID, wsUUID string) (*time.Time, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	t, ok := f.wsDeletionAnnos[wsKey{orgUUID, wsUUID}]
	if !ok {
		return nil, false, nil
	}
	tt := t
	return &tt, true, nil
}

func (f *fakeOps) SetWorkspaceDeletionAnnotation(_ context.Context, orgUUID, wsUUID string, at time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.wsDeletionAnnos[wsKey{orgUUID, wsUUID}] = at
	return nil
}

func (f *fakeOps) ClearWorkspaceDeletionAnnotation(_ context.Context, orgUUID, wsUUID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.wsDeletionAnnos, wsKey{orgUUID, wsUUID})
	return nil
}

func (f *fakeOps) GetChildWorkspaceClusterName(_ context.Context, orgUUID, wsUUID string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.childWorkspaces[orgUUID][wsUUID]; !ok {
		return "", fmt.Errorf("workspace not found")
	}
	// Deterministic fake cluster name; the kubeconfig handler only needs a
	// non-empty value to build a valid /clusters/<name> URL in tests.
	return "fake-" + wsUUID, nil
}

func (f *fakeOps) ListMCPServers(_ context.Context, _ string) ([]kcp.MCPServerInfo, error) {
	return []kcp.MCPServerInfo{{Name: "default", Phase: "Ready"}}, nil
}

func (f *fakeOps) CreateMCPServer(_ context.Context, _, _, _, _ string, _ bool) error { return nil }

func (f *fakeOps) UpdateMCPServer(_ context.Context, _, _, _, _ string, _ bool) error { return nil }

func (f *fakeOps) DeleteMCPServer(_ context.Context, _, _ string) error { return nil }

func (f *fakeOps) GetMCPServerToken(_ context.Context, clusterName, name string) (string, error) {
	if clusterName == "" || name == "" {
		return "", nil
	}
	return "fake-mcp-token", nil
}

func (f *fakeOps) EnsureChildWorkspaceAdmin(_ context.Context, orgUUID, wsUUID, rbacIdentity string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.childWorkspaces[orgUUID][wsUUID]; !ok {
		return fmt.Errorf("workspace not found")
	}
	if f.workspaceAdmins == nil {
		f.workspaceAdmins = map[wsKey]map[string]bool{}
	}
	key := wsKey{orgUUID, wsUUID}
	if f.workspaceAdmins[key] == nil {
		f.workspaceAdmins[key] = map[string]bool{}
	}
	f.workspaceAdmins[key][rbacIdentity] = true
	return nil
}

// ===== test fixtures =====

func newTestScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	s := runtime.NewScheme()
	if err := tenancyv1alpha1.AddToScheme(s); err != nil {
		t.Fatalf("scheme: %v", err)
	}
	return s
}

func newTestManager(t *testing.T, objects ...runtime.Object) (*Manager, *fakeOps, dynamic.Interface) {
	t.Helper()
	scheme := newTestScheme(t)
	gvrToListKind := map[schema.GroupVersionResource]string{
		kedgeclient.OrganizationGVR:        "OrganizationList",
		kedgeclient.UserGVR:                "UserList",
		kedgeclient.UserMembershipIndexGVR: "UserMembershipIndexList",
	}
	// Use the customListKinds variant with no seed objects, then seed
	// via the dynamic client so the GVR/Kind mapping is exercised
	// the same way our handlers exercise it (via Create on the
	// underlying client). Lets us seed UMI objects whose default
	// pluralization (usermembershipindexs) wouldn't match the
	// canonical GVR (usermembershipindices).
	// Users are seeded as typed objects at construction so the fake's
	// typed List reactor can convert them back — resolveUser relies on
	// List for email/rbacIdentity lookups. Objects seeded post-hoc via
	// the dynamic client's Create (below) are stored unstructured, which
	// the fake's typed List can't convert. Org/UMI don't need List, and
	// UMI in particular must be seeded via Create to dodge the fake's
	// default (wrong) pluralization — see the comment above.
	var typedSeed []runtime.Object
	var createSeed []runtime.Object
	for _, obj := range objects {
		if _, isUser := obj.(*tenancyv1alpha1.User); isUser {
			typedSeed = append(typedSeed, obj)
		} else {
			createSeed = append(createSeed, obj)
		}
	}
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(scheme, gvrToListKind, typedSeed...)
	client := kedgeclient.NewFromDynamic(dyn)
	for _, obj := range createSeed {
		seedObject(t, client, obj)
	}
	ops := newFakeOps()
	mgr := NewManager(client, ops)
	return mgr, ops, dyn
}

// seedObject writes a fixture into the fake via the typed client
// surface so the GVR mapping is identical to what handlers use.
func seedObject(t *testing.T, client *kedgeclient.Client, obj runtime.Object) {
	t.Helper()
	ctx := context.Background()
	switch o := obj.(type) {
	case *tenancyv1alpha1.Organization:
		if _, err := client.Organizations().Create(ctx, o, metav1.CreateOptions{}); err != nil {
			t.Fatalf("seeding Organization: %v", err)
		}
	case *tenancyv1alpha1.User:
		if _, err := client.Users().Create(ctx, o, metav1.CreateOptions{}); err != nil {
			t.Fatalf("seeding User: %v", err)
		}
	case *tenancyv1alpha1.UserMembershipIndex:
		if _, err := client.UserMembershipIndices().Create(ctx, o, metav1.CreateOptions{}); err != nil {
			t.Fatalf("seeding UMI: %v", err)
		}
	default:
		t.Fatalf("seedObject: unsupported type %T", obj)
	}
}

func newTestServer(t *testing.T, mgr *Manager, tc tenant.TenantContext) *httptest.Server {
	t.Helper()
	h := NewHandler(mgr)
	r := mux.NewRouter()

	userOnly := r.PathPrefix("/api").Subrouter()
	userOnly.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			next.ServeHTTP(w, req.WithContext(tenant.WithContext(req.Context(), tenant.TenantContext{User: tc.User})))
		})
	})
	h.RegisterUserOnly(userOnly)

	tenantSub := r.PathPrefix("/api/orgs").Subrouter()
	tenantSub.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			next.ServeHTTP(w, req.WithContext(tenant.WithContext(req.Context(), tc)))
		})
	})
	h.RegisterTenantScoped(tenantSub)
	return httptest.NewServer(r)
}

func adminTC(user, org, ws string) tenant.TenantContext {
	return tenant.TenantContext{User: user, OrgUUID: org, WorkspaceUUID: ws, Role: tenancyv1alpha1.MembershipRoleAdmin}
}

func memberTC(user, org, ws string) tenant.TenantContext {
	return tenant.TenantContext{User: user, OrgUUID: org, WorkspaceUUID: ws, Role: tenancyv1alpha1.MembershipRoleMember}
}

func TestListOrgs_SuppressesSoftDeleted(t *testing.T) {
	now := metav1.NewTime(time.Now())
	umi := &tenancyv1alpha1.UserMembershipIndex{
		ObjectMeta: metav1.ObjectMeta{Name: "alice"},
		Spec: tenancyv1alpha1.UserMembershipIndexSpec{
			Entries: []tenancyv1alpha1.MembershipIndexEntry{
				{OrgUUID: "org-a", OrgDisplayName: "A", Role: "admin"},
				{OrgUUID: "org-b", OrgDisplayName: "B", Role: "member", SoftDeletedAt: &now},
			},
		},
	}
	mgr, _, _ := newTestManager(t, umi)
	srv := newTestServer(t, mgr, adminTC("alice", "", ""))
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/api/orgs")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status: got %d", resp.StatusCode)
	}
	var list ListResponse[OrgView]
	if err := json.NewDecoder(resp.Body).Decode(&list); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(list.Items) != 1 || list.Items[0].UUID != "org-a" {
		t.Errorf("Items: %#v", list.Items)
	}
}

func TestCreateOrg_ValidatesAndPersists(t *testing.T) {
	mgr, ops, _ := newTestManager(t)
	srv := newTestServer(t, mgr, adminTC("alice", "", ""))
	defer srv.Close()

	// missing displayName → 400
	body, _ := json.Marshal(CreateOrgRequest{})
	resp, _ := http.Post(srv.URL+"/api/orgs", "application/json", jsonBody(body))
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("missing displayName: got %d, want 400", resp.StatusCode)
	}
	_ = resp.Body.Close()

	// happy path
	body, _ = json.Marshal(CreateOrgRequest{DisplayName: "acme"})
	resp, _ = http.Post(srv.URL+"/api/orgs", "application/json", jsonBody(body))
	if resp.StatusCode != http.StatusCreated {
		t.Errorf("happy: got %d, want 201", resp.StatusCode)
	}
	var view OrgView
	if err := json.NewDecoder(resp.Body).Decode(&view); err != nil {
		t.Fatalf("decode: %v", err)
	}
	_ = resp.Body.Close()
	if view.DisplayName != "acme" || view.UUID == "" {
		t.Errorf("view: %#v", view)
	}
	if !ops.orgWorkspaces[view.UUID] {
		t.Error("EnsureOrgWorkspace not called")
	}
	if ops.orgMemberships[view.UUID]["alice"] != "admin" {
		t.Errorf("alice's membership: %v", ops.orgMemberships[view.UUID])
	}
}

func TestDeleteAndUndeleteOrg(t *testing.T) {
	org := &tenancyv1alpha1.Organization{
		ObjectMeta: metav1.ObjectMeta{Name: "org-a"},
		Spec:       tenancyv1alpha1.OrganizationSpec{DisplayName: "A"},
	}
	mgr, _, _ := newTestManager(t, org)
	srv := newTestServer(t, mgr, adminTC("alice", "org-a", ""))
	defer srv.Close()

	req, _ := http.NewRequest(http.MethodDelete, srv.URL+"/api/orgs/org-a", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("DELETE: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Errorf("DELETE status: got %d, want 200", resp.StatusCode)
	}
	_ = resp.Body.Close()

	// Confirm DeletionRequestedAt set.
	got, _ := mgr.client.Organizations().Get(context.Background(), "org-a", metav1.GetOptions{})
	if got.Status.DeletionRequestedAt == nil {
		t.Error("expected DeletionRequestedAt set")
	}

	// Undelete
	req, _ = http.NewRequest(http.MethodPost, srv.URL+"/api/orgs/org-a/undelete", nil)
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("undelete: %v", err)
	}
	_ = resp.Body.Close()
	got, _ = mgr.client.Organizations().Get(context.Background(), "org-a", metav1.GetOptions{})
	if got.Status.DeletionRequestedAt != nil {
		t.Error("expected DeletionRequestedAt cleared")
	}
}

func TestDeleteOrg_RequiresAdmin(t *testing.T) {
	org := &tenancyv1alpha1.Organization{
		ObjectMeta: metav1.ObjectMeta{Name: "org-a"},
		Spec:       tenancyv1alpha1.OrganizationSpec{DisplayName: "A"},
	}
	mgr, _, _ := newTestManager(t, org)
	srv := newTestServer(t, mgr, memberTC("alice", "org-a", ""))
	defer srv.Close()

	req, _ := http.NewRequest(http.MethodDelete, srv.URL+"/api/orgs/org-a", nil)
	resp, _ := http.DefaultClient.Do(req)
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("status: got %d, want 403", resp.StatusCode)
	}
	_ = resp.Body.Close()
}

// ===== Workspace tests =====

func TestCreateWorkspace_RestrictedToAdmin(t *testing.T) {
	org := &tenancyv1alpha1.Organization{
		ObjectMeta: metav1.ObjectMeta{Name: "org-a"},
		Spec: tenancyv1alpha1.OrganizationSpec{
			DisplayName: "A", WorkspaceCreation: tenancyv1alpha1.WorkspaceCreationAdmin,
		},
	}
	mgr, _, _ := newTestManager(t, org)
	srv := newTestServer(t, mgr, memberTC("bob", "org-a", ""))
	defer srv.Close()

	body, _ := json.Marshal(CreateWorkspaceRequest{DisplayName: "ws"})
	resp, _ := http.Post(srv.URL+"/api/orgs/org-a/workspaces", "application/json", jsonBody(body))
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("status: got %d, want 403", resp.StatusCode)
	}
	_ = resp.Body.Close()
}

func TestCreateWorkspace_HappyPath(t *testing.T) {
	org := &tenancyv1alpha1.Organization{
		ObjectMeta: metav1.ObjectMeta{Name: "org-a"},
		Spec: tenancyv1alpha1.OrganizationSpec{
			DisplayName:       "A",
			WorkspaceCreation: tenancyv1alpha1.WorkspaceCreationMembers,
		},
	}
	alice := &tenancyv1alpha1.User{
		ObjectMeta: metav1.ObjectMeta{Name: "alice"},
		Spec:       tenancyv1alpha1.UserSpec{Email: "alice@example.com", RBACIdentity: "kedge:alice@example.com"},
	}
	mgr, ops, _ := newTestManager(t, org, alice)
	srv := newTestServer(t, mgr, adminTC("alice", "org-a", ""))
	defer srv.Close()

	body, _ := json.Marshal(CreateWorkspaceRequest{DisplayName: "platform"})
	resp, err := http.Post(srv.URL+"/api/orgs/org-a/workspaces", "application/json", jsonBody(body))
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("status: got %d, want 201", resp.StatusCode)
	}
	var view WorkspaceView
	if err := json.NewDecoder(resp.Body).Decode(&view); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if view.DisplayName != "platform" || view.UUID == "" {
		t.Errorf("view: %#v", view)
	}
	if !ops.childWorkspaces["org-a"][view.UUID] {
		t.Error("EnsureChildWorkspace not called")
	}
	if ops.kedgeBindingCalls[wsKey{"org-a", view.UUID}] != 1 {
		t.Errorf("kedge binding call count: got %d", ops.kedgeBindingCalls[wsKey{"org-a", view.UUID}])
	}
	if ops.wsDisplayNames[wsKey{"org-a", view.UUID}] != "platform" {
		t.Errorf("display name not set: %v", ops.wsDisplayNames)
	}
	// Regression guard for the v0.0.63 workspace-switch 403: createWorkspace
	// must seed the caller's cluster-admin CRB; without it the freshly-
	// minted workspace 403s from the GraphQL gateway the moment the user
	// switches into it.
	if !ops.workspaceAdmins[wsKey{"org-a", view.UUID}]["kedge:alice@example.com"] {
		t.Errorf("EnsureChildWorkspaceAdmin not called for caller; admins=%v",
			ops.workspaceAdmins[wsKey{"org-a", view.UUID}])
	}
}

func TestDeleteWorkspace_SetsAnnotation(t *testing.T) {
	mgr, ops, _ := newTestManager(t)
	_ = ops.EnsureChildWorkspace(context.Background(), "org-a", "ws-1")
	srv := newTestServer(t, mgr, adminTC("alice", "org-a", "ws-1"))
	defer srv.Close()

	req, _ := http.NewRequest(http.MethodDelete, srv.URL+"/api/orgs/org-a/workspaces/ws-1", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("DELETE: %v", err)
	}
	if err := resp.Body.Close(); err != nil {
		t.Fatalf("close DELETE response body: %v", err)
	}
	if resp.StatusCode != http.StatusNoContent {
		t.Errorf("status: got %d, want 204", resp.StatusCode)
	}
	if _, ok := ops.wsDeletionAnnos[wsKey{"org-a", "ws-1"}]; !ok {
		t.Error("deletion annotation not set")
	}
}

func TestAddOrgMembership_UpdatesCRAndUMI(t *testing.T) {
	org := &tenancyv1alpha1.Organization{
		ObjectMeta: metav1.ObjectMeta{Name: "org-a"},
		Spec:       tenancyv1alpha1.OrganizationSpec{DisplayName: "A"},
	}
	bob := &tenancyv1alpha1.User{
		ObjectMeta: metav1.ObjectMeta{Name: "bob"},
		Spec:       tenancyv1alpha1.UserSpec{Email: "bob@example.com"},
	}
	mgr, ops, _ := newTestManager(t, org, bob)
	srv := newTestServer(t, mgr, adminTC("alice", "org-a", ""))
	defer srv.Close()

	body, _ := json.Marshal(MembershipAddRequest{User: "bob", Role: "member"})
	resp, err := http.Post(srv.URL+"/api/orgs/org-a/memberships", "application/json", jsonBody(body))
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusCreated {
		t.Errorf("status: got %d, want 201", resp.StatusCode)
	}
	if ops.orgMemberships["org-a"]["bob"] != "member" {
		t.Errorf("membership: %v", ops.orgMemberships)
	}
	idx, _ := mgr.client.UserMembershipIndices().Get(context.Background(), "bob", metav1.GetOptions{})
	if len(idx.Spec.Entries) != 1 || idx.Spec.Entries[0].Role != "member" {
		t.Errorf("UMI: %#v", idx)
	}
}

// TestAddOrgMembership_ResolvesEmail covers the prod bug where an admin
// typed an email into "Add member": the email must resolve to the User
// CR name so the Membership CR / UMI are named with a valid RFC1123
// name instead of tripping a kcp Invalid error (opaque 400).
func TestAddOrgMembership_ResolvesEmail(t *testing.T) {
	org := &tenancyv1alpha1.Organization{
		ObjectMeta: metav1.ObjectMeta{Name: "org-a"},
		Spec:       tenancyv1alpha1.OrganizationSpec{DisplayName: "A"},
	}
	bob := &tenancyv1alpha1.User{
		ObjectMeta: metav1.ObjectMeta{Name: "user-bob"},
		Spec:       tenancyv1alpha1.UserSpec{Email: "Bob@Example.com", RBACIdentity: "kedge:bob@example.com"},
	}
	mgr, ops, _ := newTestManager(t, org, bob)
	srv := newTestServer(t, mgr, adminTC("alice", "org-a", ""))
	defer srv.Close()

	// Mixed-case email exercises the case-insensitive match.
	body, _ := json.Marshal(MembershipAddRequest{User: "bob@example.com", Role: "admin"})
	resp, err := http.Post(srv.URL+"/api/orgs/org-a/memberships", "application/json", jsonBody(body))
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusCreated {
		t.Errorf("status: got %d, want 201", resp.StatusCode)
	}
	// Everything must be keyed on the User CR name, not the email.
	if ops.orgMemberships["org-a"]["user-bob"] != "admin" {
		t.Errorf("membership not keyed by User name: %v", ops.orgMemberships)
	}
	idx, err := mgr.client.UserMembershipIndices().Get(context.Background(), "user-bob", metav1.GetOptions{})
	if err != nil || len(idx.Spec.Entries) != 1 {
		t.Errorf("UMI not named after User CR: %#v (err %v)", idx, err)
	}
}

// TestAddOrgMembership_UnknownUser404 asserts an unresolvable identifier
// gets a clean 404 rather than a leaked kcp error.
func TestAddOrgMembership_UnknownUser404(t *testing.T) {
	org := &tenancyv1alpha1.Organization{
		ObjectMeta: metav1.ObjectMeta{Name: "org-a"},
		Spec:       tenancyv1alpha1.OrganizationSpec{DisplayName: "A"},
	}
	mgr, _, _ := newTestManager(t, org)
	srv := newTestServer(t, mgr, adminTC("alice", "org-a", ""))
	defer srv.Close()

	body, _ := json.Marshal(MembershipAddRequest{User: "nobody@example.com", Role: "member"})
	resp, err := http.Post(srv.URL+"/api/orgs/org-a/memberships", "application/json", jsonBody(body))
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("status: got %d, want 404", resp.StatusCode)
	}
}

// TestWorkspaceMembership_AddGrantsAccess covers granting a user access
// to an existing workspace: the add path resolves the email, writes a
// workspace-scope UMI row (keyed by the User CR name), and grants the
// matching kcp RBAC so the GraphQL gateway lets them in.
//
// The list projection (listWorkspaceMemberships across all UMIs) can't
// be exercised here: dynamicfake's typed List can't convert objects
// created at runtime via the dynamic client (they're stored
// unstructured — Get works, typed List doesn't). Against real kcp the
// dynamic client returns an UnstructuredList that round-trips fine.
func TestWorkspaceMembership_AddGrantsAccess(t *testing.T) {
	org := &tenancyv1alpha1.Organization{
		ObjectMeta: metav1.ObjectMeta{Name: "org-a"},
		Spec:       tenancyv1alpha1.OrganizationSpec{DisplayName: "A"},
	}
	bob := &tenancyv1alpha1.User{
		ObjectMeta: metav1.ObjectMeta{Name: "user-bob"},
		Spec:       tenancyv1alpha1.UserSpec{Email: "bob@example.com", RBACIdentity: "kedge:bob@example.com"},
	}
	mgr, ops, _ := newTestManager(t, org, bob)
	// The workspace must exist for the RBAC grant + display-name lookup.
	if err := ops.EnsureChildWorkspace(context.Background(), "org-a", "ws-1"); err != nil {
		t.Fatalf("seed workspace: %v", err)
	}
	srv := newTestServer(t, mgr, adminTC("alice", "org-a", "ws-1"))
	defer srv.Close()

	// Add by email — must resolve to the User CR name.
	body, _ := json.Marshal(MembershipAddRequest{User: "bob@example.com", Role: "member"})
	resp, err := http.Post(srv.URL+"/api/orgs/org-a/workspaces/ws-1/memberships", "application/json", jsonBody(body))
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	if resp.StatusCode != http.StatusCreated {
		t.Errorf("add status: got %d, want 201", resp.StatusCode)
	}
	_ = resp.Body.Close()

	// kcp RBAC granted under the resolved rbacIdentity.
	if !ops.workspaceAdmins[wsKey{"org-a", "ws-1"}]["kedge:bob@example.com"] {
		t.Errorf("workspace RBAC not granted: %v", ops.workspaceAdmins)
	}
	// UMI ws-scope row keyed by User CR name.
	idx, err := mgr.client.UserMembershipIndices().Get(context.Background(), "user-bob", metav1.GetOptions{})
	if err != nil || len(idx.Spec.Entries) != 1 || idx.Spec.Entries[0].WorkspaceUUID != "ws-1" {
		t.Errorf("UMI ws-row missing: %#v (err %v)", idx, err)
	}
}

func TestDeleteOrgMembership_RemovesFromBootstrapper(t *testing.T) {
	mgr, ops, _ := newTestManager(t)
	_ = ops.EnsureOrgMembership(context.Background(), "org-a", "bob", "member")
	srv := newTestServer(t, mgr, adminTC("alice", "org-a", ""))
	defer srv.Close()

	req, _ := http.NewRequest(http.MethodDelete, srv.URL+"/api/orgs/org-a/memberships/bob", nil)
	resp, _ := http.DefaultClient.Do(req)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Errorf("status: got %d, want 204", resp.StatusCode)
	}
	if _, ok := ops.orgMemberships["org-a"]["bob"]; ok {
		t.Error("Membership CR not deleted")
	}
}

func TestDeleteOrgMembership_CascadeFlagReadsQueryParam(t *testing.T) {
	mgr, ops, _ := newTestManager(t)
	_ = ops.EnsureOrgMembership(context.Background(), "org-a", "bob", "member")
	srv := newTestServer(t, mgr, adminTC("alice", "org-a", ""))
	defer srv.Close()

	req, _ := http.NewRequest(http.MethodDelete, srv.URL+"/api/orgs/org-a/memberships/bob?cascade=true", nil)
	resp, _ := http.DefaultClient.Do(req)
	_ = resp.Body.Close()
	// 204 even when there's no UMI to scrub; the handler walks the
	// cascade branch and short-circuits cleanly.
	if resp.StatusCode != http.StatusNoContent {
		t.Errorf("status: got %d, want 204", resp.StatusCode)
	}
}

func TestPatchOrgMembershipRole(t *testing.T) {
	mgr, ops, _ := newTestManager(t)
	_ = ops.EnsureOrgMembership(context.Background(), "org-a", "bob", "member")
	srv := newTestServer(t, mgr, adminTC("alice", "org-a", ""))
	defer srv.Close()

	body, _ := json.Marshal(MembershipPatchRequest{Role: "admin"})
	req, _ := http.NewRequest(http.MethodPatch, srv.URL+"/api/orgs/org-a/memberships/bob", jsonBody(body))
	req.Header.Set("Content-Type", "application/json")
	resp, _ := http.DefaultClient.Do(req)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("status: got %d, want 200", resp.StatusCode)
	}
	if ops.orgMemberships["org-a"]["bob"] != "admin" {
		t.Errorf("CR not patched: %v", ops.orgMemberships)
	}
}

func TestSelfLeaveOrg(t *testing.T) {
	mgr, ops, _ := newTestManager(t)
	_ = ops.EnsureOrgMembership(context.Background(), "org-a", "bob", "member")
	srv := newTestServer(t, mgr, memberTC("bob", "org-a", ""))
	defer srv.Close()

	req, _ := http.NewRequest(http.MethodDelete, srv.URL+"/api/orgs/org-a/memberships/me", nil)
	resp, _ := http.DefaultClient.Do(req)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Errorf("status: got %d, want 204", resp.StatusCode)
	}
	if _, ok := ops.orgMemberships["org-a"]["bob"]; ok {
		t.Error("Membership CR not deleted")
	}
}

// ===== Provider enable tests =====

func TestEnableProvider_BlocksMissingDependencies(t *testing.T) {
	mgr, ops, _ := newTestManager(t)
	reg := hubproviders.NewRegistry()
	reg.Upsert(hubproviders.Provider{
		Name:          "app-studio",
		APIExportPath: "root:providers:app-studio",
		APIExportName: "app-studio",
		Dependencies:  []hubproviders.Dependency{{Name: "code"}},
	})
	mgr.WithProviderRegistry(reg)
	srv := newTestServer(t, mgr, adminTC("alice", "org-a", "ws-1"))
	defer srv.Close()

	body, _ := json.Marshal(EnableProviderRequest{})
	resp, err := http.Post(srv.URL+"/api/orgs/org-a/workspaces/ws-1/providers/app-studio/enable", "application/json", jsonBody(body))
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("status: got %d, want 409", resp.StatusCode)
	}
	payload, _ := io.ReadAll(resp.Body)
	if !bytes.Contains(payload, []byte("code")) {
		t.Fatalf("response %q does not mention missing dependency code", payload)
	}
	if ops.providerBindCalls[wsKey{"org-a", "ws-1"}] != 0 {
		t.Fatalf("EnsureProviderAPIBinding called despite missing dependency")
	}
}

func TestEnableProvider_AllowsSatisfiedDependencies(t *testing.T) {
	mgr, ops, _ := newTestManager(t)
	key := wsKey{"org-a", "ws-1"}
	ops.providerBindings[key] = map[string]string{"code": "code"}
	reg := hubproviders.NewRegistry()
	reg.Upsert(hubproviders.Provider{
		Name:          "app-studio",
		APIExportPath: "root:providers:app-studio",
		APIExportName: "app-studio",
		Dependencies:  []hubproviders.Dependency{{Name: "code"}},
	})
	mgr.WithProviderRegistry(reg)
	srv := newTestServer(t, mgr, adminTC("alice", "org-a", "ws-1"))
	defer srv.Close()

	body, _ := json.Marshal(EnableProviderRequest{})
	resp, err := http.Post(srv.URL+"/api/orgs/org-a/workspaces/ws-1/providers/app-studio/enable", "application/json", jsonBody(body))
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		payload, _ := io.ReadAll(resp.Body)
		t.Fatalf("status: got %d, want 200; body=%s", resp.StatusCode, payload)
	}
	if ops.providerBindCalls[key] != 1 {
		t.Fatalf("EnsureProviderAPIBinding calls = %d, want 1", ops.providerBindCalls[key])
	}
	if got := ops.providerBindings[key]["app-studio"]; got != "app-studio" {
		t.Fatalf("provider binding = %q, want app-studio", got)
	}
}

// ===== User self-delete =====

func TestDeleteSelfUser_StampsTimestamp(t *testing.T) {
	u := &tenancyv1alpha1.User{ObjectMeta: metav1.ObjectMeta{Name: "alice"}}
	mgr, _, _ := newTestManager(t, u)
	srv := newTestServer(t, mgr, adminTC("alice", "", ""))
	defer srv.Close()

	req, _ := http.NewRequest(http.MethodDelete, srv.URL+"/api/users/me", nil)
	resp, _ := http.DefaultClient.Do(req)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Errorf("status: got %d, want 204", resp.StatusCode)
	}
	got, _ := mgr.client.Users().Get(context.Background(), "alice", metav1.GetOptions{})
	if got.Status.DeletionRequestedAt == nil {
		t.Error("DeletionRequestedAt not set")
	}
}

// ===== Kubeconfig download tests =====

func TestDownloadKubeconfig_InstallVariant(t *testing.T) {
	mgr, ops, _ := newTestManager(t)
	_ = ops.EnsureChildWorkspace(context.Background(), "org-a", "ws-1")
	mgr.WithKubeconfig(KubeconfigConfig{
		HubExternalURL: "https://hub.test",
		OIDCIssuerURL:  "https://issuer.test",
		OIDCClientID:   "test-client",
	})
	srv := newTestServer(t, mgr, adminTC("alice", "org-a", "ws-1"))
	defer srv.Close()

	cases := []struct {
		name        string
		query       string
		wantStatus  int
		wantCommand string // empty if status != 200
	}{
		{"default", "", http.StatusOK, "kedge"},
		{"explicit kedge", "?install=kedge", http.StatusOK, "kedge"},
		{"krew alias", "?install=krew", http.StatusOK, "kubectl-kedge"},
		{"explicit kubectl-kedge", "?install=kubectl-kedge", http.StatusOK, "kubectl-kedge"},
		{"unknown", "?install=bogus", http.StatusBadRequest, ""},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resp, err := http.Get(srv.URL + "/api/orgs/org-a/workspaces/ws-1/kubeconfig" + tc.query)
			if err != nil {
				t.Fatalf("GET: %v", err)
			}
			defer func() { _ = resp.Body.Close() }()
			if resp.StatusCode != tc.wantStatus {
				t.Fatalf("status: got %d, want %d", resp.StatusCode, tc.wantStatus)
			}
			if tc.wantStatus != http.StatusOK {
				return
			}
			body, _ := io.ReadAll(resp.Body)
			// We only assert on the substring; a YAML parse here would drag in
			// clientcmd just to re-check what the handler already produces.
			want := "command: " + tc.wantCommand
			if !bytes.Contains(body, []byte(want)) {
				t.Errorf("response missing %q\nbody:\n%s", want, body)
			}
		})
	}
}

// ===== helpers =====

// jsonBody wraps a []byte as a Reader for http.Post.
func jsonBody(b []byte) io.Reader { return bytes.NewReader(b) }
